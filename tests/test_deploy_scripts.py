"""Offline safety and lifecycle tests. No running Docker daemon required."""
import base64
import copy
import importlib.util
import json
import os
import shutil
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("compose_guard", ROOT / "deploy/compose_guard.py")
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)
KEY = base64.b64encode(b"k" * 32).decode()


def config():
    return {
        "name": "legacy-project",
        "services": {
            "app": {
                "build": {"context": "/checkout"},
                "environment": {"MASTER_KEY": KEY, "ADMIN_USER": "admin", "ADMIN_PASSWORD": "pw",
                                "DATABASE_URL": "postgres://audit:dbpw@db:5432/audit?sslmode=disable",
                                "AUDIT_MODEL_CONCURRENCY": "16"},
                "ports": [{"host_ip": "127.0.0.1", "published": "8090", "target": 8090}],
            },
            "db": {
                "image": "postgres:17-alpine",
                "environment": {"POSTGRES_USER": "audit", "POSTGRES_DB": "audit", "POSTGRES_PASSWORD": "dbpw"},
                "volumes": [{"type": "volume", "source": "audit-postgres", "target": "/var/lib/postgresql/data"}],
            },
        },
        "volumes": {"audit-postgres": {"name": "legacy-project_audit-postgres"}},
    }


class GuardTests(unittest.TestCase):
    def test_valid_credentials_and_future_build(self):
        old = config()
        guard.credentials(old)
        new = copy.deepcopy(old)
        new["services"]["app"]["build"]["context"] = "/new-checkout"
        new["services"]["app"]["environment"]["AUDIT_NEW_LIMIT"] = "10"
        guard.check_target(old, new)

    def test_refuse_unsafe_target_changes(self):
        mutations = [
            lambda c: c["volumes"]["audit-postgres"].update(name="another-volume"),
            lambda c: c["services"]["db"].update(image="postgres:18-alpine"),
            lambda c: c["services"]["app"]["environment"].update(MASTER_KEY=base64.b64encode(b"n" * 32).decode()),
            lambda c: c["services"]["app"]["environment"].update(AUDIT_MODEL_CONCURRENCY="64"),
            lambda c: c["services"]["app"]["environment"].update(OTHER_ENV="unexpected"),
            lambda c: c["services"]["app"]["ports"][0].update(host_ip="0.0.0.0"),
            lambda c: c["services"]["app"].update(volumes=[{"target": "/data"}]),
            lambda c: c["services"]["app"].update(env_file=[{"path": "secrets.env"}]),
            lambda c: c["services"]["app"].pop("build"),
        ]
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                old, new = config(), config()
                mutate(new)
                with self.assertRaises(guard.GuardError):
                    guard.check_target(old, new)

    def test_backup_paths_and_port_drift(self):
        db = {"Mounts": [{"Source": "/var/lib/postgres/data"}]}
        guard.check_backup_path("/checkout", "/safe-backups", db)
        for path in ("/checkout", "/var/lib/postgres/data", "/var/lib/postgres/data/backups"):
            with self.subTest(path=path), self.assertRaises(guard.GuardError):
                guard.check_backup_path("/checkout", path, db)
        with self.assertRaises(guard.GuardError):
            guard.check_ports(config()["services"]["app"], {"HostConfig": {"PortBindings": {}}})

    def test_invalid_credentials_never_echo_secret(self):
        for key in ("super-secret-but-invalid", base64.b64encode(b"short").decode(), "!!!!"):
            c = config()
            c["services"]["app"]["environment"]["MASTER_KEY"] = key
            with self.assertRaises(guard.GuardError) as error:
                guard.credentials(c)
            self.assertNotIn(key, str(error.exception))

    def test_current_environment_and_volume_drift(self):
        c = config()
        c["services"]["app"]["environment"]["ADMIN_PASSWORD"] = "pw$$literal-dollar"
        app = {"Config": {"Env": [f"{k}={guard.literal_env(v)}" for k, v in c["services"]["app"]["environment"].items()]}, "Mounts": [], "HostConfig": {"PortBindings": {"8090/tcp": [{"HostIp": "127.0.0.1", "HostPort": "8090"}]}}}
        db = {"Config": {"Image": "postgres:17-alpine", "Env": [f"{k}={v}" for k, v in c["services"]["db"]["environment"].items()]}}
        guard.check_current(c, app, db, "legacy-project_audit-postgres")
        with self.assertRaises(guard.GuardError):
            guard.check_current(c, app, db, "other-volume")
        app["Config"]["Env"] = ["MASTER_KEY=do-not-print-this"]
        with self.assertRaises(guard.GuardError) as error:
            guard.check_current(c, app, db, "legacy-project_audit-postgres")
        self.assertNotIn("do-not-print-this", str(error.exception))


MOCK_DOCKER = r'''#!/usr/bin/env python3
import base64, json, os, pathlib, sys
root = pathlib.Path(os.environ["FAKE_ROOT"])
state = pathlib.Path(os.environ["FAKE_STATE"])
mode = os.environ.get("FAKE_MODE", "update")
a = sys.argv[1:]
with (state / "calls").open("a") as f:
    f.write(json.dumps(["docker"] + a) + "\n")
def die(): sys.exit(1)
def env():
    path = root / ".env"
    return dict(line.split("=", 1) for line in path.read_text().splitlines() if line and not line.startswith("#")) if path.exists() else {}
def project(): return "deepseek-audit" if mode.startswith("install") else "legacy-project"
def cfg(exported=True):
    e = env()
    app = {"MASTER_KEY": e.get("MASTER_KEY", ""), "ADMIN_USER": e.get("ADMIN_USER", "admin"),
           "ADMIN_PASSWORD": e.get("ADMIN_PASSWORD", ""), "PUBLIC_URL": e.get("PUBLIC_URL", "http://localhost:8090"),
           "DATABASE_URL": "postgres://audit:" + e.get("POSTGRES_PASSWORD", "") + "@db:5432/audit?sslmode=disable"}
    # Exported values must have been cleared by compose().
    for k in app:
        if exported and k in os.environ: app[k] = os.environ[k]
    c = {"name": project(), "services": {
       "app": {"build": {"context": str(root)}, "environment": app},
       "db": {"image": "postgres:17-alpine", "environment": {"POSTGRES_USER": "audit", "POSTGRES_DB": "audit", "POSTGRES_PASSWORD": e.get("POSTGRES_PASSWORD", "")},
              "volumes": [{"type": "volume", "source": "audit-postgres", "target": "/var/lib/postgresql/data"}]}},
       "volumes": {"audit-postgres": {"name": project() + "_audit-postgres"}}}
    if mode == "target-drift" and (state / "merged").exists():
        c["volumes"]["audit-postgres"]["name"] = "different-volume"
    return c
def item(cid):
    service = "db" if cid == "db-original" else "app"
    data = cfg(False)
    actual_env = dict(data["services"][service]["environment"])
    if mode == "env-drift" and service == "app": actual_env["MASTER_KEY"] = "wrong-key"
    if mode == "install-resume-drift" and service == "app": actual_env["MASTER_KEY"] = "old-key"
    mounts = [] if service == "app" else [{"Type": "volume", "Destination": "/var/lib/postgresql/data", "Name": project() + "_audit-postgres", "Source": "/var/lib/docker/volumes/audit-postgres/_data"}]
    return {"Id": cid, "Image": "sha256:old-image", "Config": {"Image": "postgres:17-alpine" if service == "db" else project() + "-app",
      "Env": [k + "=" + v for k,v in actual_env.items()], "Labels": {
      "com.docker.compose.service": service, "com.docker.compose.project": project(),
      "com.docker.compose.project.working_dir": str(root) if mode != "wrong-directory" else "/another/checkout",
      "com.docker.compose.project.config_files": str(root / "compose.yaml") + (",override.yaml" if mode == "override" else "")}},
      "Mounts": mounts, "State": {"Status": "running", "Health": {"Status": "unhealthy" if (mode == "unhealthy-old" or (mode == "unhealthy-new" and cid == "app-new")) else "healthy"}}}
if a[0] == "info": sys.exit(0)
if a[0] == "ps":
    if mode in ("install", "install-build-failure", "install-volume"): sys.exit(0)
    if any("service=db" in x for x in a): print("db-original")
    elif any("service=app" in x for x in a): print("app-original")
    else: print("app-original\ndb-original")
    sys.exit(0)
if a[0] == "volume":
    if a[1] == "ls":
        if mode == "install-volume": print("legacy-project_audit-postgres")
        sys.exit(0)
    if mode in ("install", "install-build-failure"): die()
    if a[-1] == "deepseek-audit_audit-postgres" and not mode.startswith("install"): die()
    print(json.dumps([{"Labels": {"com.docker.compose.project": project(), "com.docker.compose.volume": "audit-postgres"}}])); sys.exit(0)
if a[0] == "inspect":
    cid = a[-1]
    if "--format" in a:
        fmt = a[a.index("--format") + 1]
        print(item(cid)["Image"] if fmt == "{{.Image}}" else item(cid)["State"]["Health"]["Status"])
    else: print(json.dumps([item(cid)]))
    sys.exit(0)
if a[0] in ("image", "start"): sys.exit(0)
if a[0] == "exec":
    if "pg_dump" in a:
        if mode == "dump-failure": die()
        print("test-logical-backup")
    elif "pg_restore" in a:
        sys.stdin.read()
        if mode == "archive-failure": die()
        print("readable archive")
    else: die()
    sys.exit(0)
if a[0] == "compose":
    a = a[1:]
    if a == ["version"]: sys.exit(0)
    files = []
    while a and a[0].startswith("-"):
        if a[0] == "-f": files.append(a[1])
        a = a[2:]
    command = a[0]
    rollback = any(x.endswith("rollback.yaml") for x in files)
    if command == "config": print(json.dumps(cfg()))
    elif command == "ps": print("db-original" if a[-1] == "db" else ("app-original" if rollback or not (state / "deployed").exists() else "app-new"))
    elif command == "build":
        if mode == "build-failure": die()
    elif command == "stop":
        (state / "stopped").touch()
    elif command == "up":
        if mode == "install-build-failure" or (mode == "up-failure" and not rollback): die()
        if rollback: (state / "rolled-back").touch()
        else: (state / "deployed").touch()
    else: die()
    sys.exit(0)
die()
'''

MOCK_GIT = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
state = pathlib.Path(os.environ["FAKE_STATE"])
mode = os.environ.get("FAKE_MODE", "update")
a = sys.argv[1:]
with (state / "calls").open("a") as f: f.write(json.dumps(["git"] + a) + "\n")
if a[:2] == ["rev-parse", "--show-toplevel"]: print(os.environ["FAKE_ROOT"])
elif a[0] == "rev-parse": print("new-revision" if (state / "merged").exists() else "old-revision")
elif a[0] == "status": print(" M user-file" if mode == "dirty" else "", end="")
elif a[0] == "branch": print("feature" if mode == "branch" else "main")
elif a[0] == "check-ignore": sys.exit(0)
elif a[0] == "fetch": sys.exit(1 if mode == "fetch-failure" else 0)
elif a[0] == "merge":
    if mode == "merge-failure": sys.exit(1)
    (state / "merged").touch()
else: sys.exit(1)
'''


class ComposeParserTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which("docker"), "Docker CLI is not installed")
    def test_real_compose_and_rollback_parse_without_daemon(self):
        if subprocess.run(["docker", "compose", "version"], stdout=subprocess.DEVNULL,
                          stderr=subprocess.DEVNULL).returncode:
            self.skipTest("Docker Compose plugin is not installed")
        env = {k: v for k, v in os.environ.items()
               if not k.startswith(("AUDIT_", "COMPOSE_"))
               and k not in ("MASTER_KEY", "ADMIN_USER", "ADMIN_PASSWORD", "POSTGRES_PASSWORD", "DATABASE_URL", "PUBLIC_URL")}
        with tempfile.TemporaryDirectory(prefix="audit-compose-parse-") as tmp:
            tmp = Path(tmp)
            envfile = tmp / ".env"
            envfile.write_text(f"MASTER_KEY={KEY}\nADMIN_USER=admin\nADMIN_PASSWORD='test$admin'\nPOSTGRES_PASSWORD=test-db\n")
            command = ["docker", "compose", "--project-directory", str(ROOT), "--env-file", str(envfile),
                       "-p", "audit-script-validation", "-f", str(ROOT / "compose.yaml")]
            data = json.loads(subprocess.check_output(command + ["config", "--no-env-resolution", "--format", "json"],
                                                      env=env, text=True))
            guard.credentials(data)
            configfile = tmp / "compose.json"
            configfile.write_text(json.dumps(data))
            rollback = tmp / "rollback.yaml"
            with rollback.open("w") as output:
                subprocess.run(["python3", "-B", str(ROOT / "deploy/compose_guard.py"), "rollback",
                                str(configfile), "deepseek-audit-backup:validation"], check=True, stdout=output)
            command[-1] = str(rollback)
            restored = json.loads(subprocess.check_output(command + ["config", "--format", "json"], env=env, text=True))
            self.assertEqual(restored["services"]["app"]["environment"], data["services"]["app"]["environment"])


class ScriptTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="audit-deploy-test-")
        self.base = Path(self.tmp.name).resolve()
        self.root = self.base / "checkout with spaces"
        self.state = self.base / "state"
        self.bin = self.base / "bin"
        for p in (self.root / "deploy", self.state, self.bin): p.mkdir(parents=True)
        for name in ("install.sh", "update.sh", "deploy/scripts-common.sh", "deploy/compose_guard.py", "compose.yaml"):
            (self.root / name).write_bytes((ROOT / name).read_bytes())
        for name, text in (("docker", MOCK_DOCKER), ("git", MOCK_GIT)):
            p = self.bin / name
            p.write_text(text)
            p.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                        FAKE_ROOT=str(self.root), FAKE_STATE=str(self.state), PYTHONDONTWRITEBYTECODE="1")
        # Do not send a user's credentials to the fixture, even if exported.
        for k in ("MASTER_KEY", "ADMIN_PASSWORD", "POSTGRES_PASSWORD", "DATABASE_URL", "ADMIN_USER", "PUBLIC_URL", "AUDIT_BACKUP_ROOT"):
            self.env.pop(k, None)
        self.original_env = f"MASTER_KEY={KEY}\nADMIN_USER=admin\nADMIN_PASSWORD=local-test-pw\nPOSTGRES_PASSWORD=local-test-db-pw\nPUBLIC_URL=http://localhost:8090\n"

    def tearDown(self): self.tmp.cleanup()

    def run_script(self, script="update.sh", mode="update", args=(), prepare=True):
        self.env["FAKE_MODE"] = mode
        if prepare and not (self.root / ".env").exists(): (self.root / ".env").write_text(self.original_env)
        return subprocess.run(["bash", str(self.root / script), *args], cwd=self.base,
                              env=self.env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=20)

    def calls(self):
        p = self.state / "calls"
        return [json.loads(line) for line in p.read_text().splitlines()] if p.exists() else []

    def has_action(self, action):
        return any(c[0] == "docker" and "compose" in c and action in c for c in self.calls())

    def test_install_and_generated_secrets(self):
        result = self.run_script("install.sh", "install", prepare=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        values = dict(line.split("=", 1) for line in (self.root / ".env").read_text().splitlines() if not line.startswith("#"))
        self.assertEqual(len(base64.b64decode(values["MASTER_KEY"])), 32)
        self.assertEqual(values["COMPOSE_PROJECT_NAME"], "deepseek-audit")
        self.assertEqual((self.root / ".env").stat().st_mode & 0o777, 0o600)
        for key in ("MASTER_KEY", "ADMIN_PASSWORD", "POSTGRES_PASSWORD"):
            self.assertNotIn(values[key], result.stdout + result.stderr)
        self.assertFalse((self.root / ".deploy.lock").exists())

    def test_install_preserves_existing_env(self):
        result = self.run_script("install.sh", "install")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.root / ".env").read_text(), self.original_env)
        self.assertFalse(self.has_action("up"))

    def test_install_refuses_existing_volume_before_generating_key(self):
        result = self.run_script("install.sh", "install-volume", prepare=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / ".env").exists())

    def test_failed_install_can_resume_with_identical_credentials(self):
        result = self.run_script("install.sh", "install-build-failure", prepare=False)
        self.assertNotEqual(result.returncode, 0)
        saved = (self.root / ".env").read_bytes()
        result = self.run_script("install.sh", "install", args=("--resume",), prepare=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(saved, (self.root / ".env").read_bytes())

    def test_resume_refuses_changed_credentials_and_unmanaged_env(self):
        result = self.run_script("install.sh", "install", args=("--resume",))
        self.assertNotEqual(result.returncode, 0)
        (self.root / ".env").write_text("# Generated by deepseek-moderation-api install.sh\nCOMPOSE_PROJECT_NAME=deepseek-audit\n" + self.original_env)
        result = self.run_script("install.sh", "install-resume-drift", args=("--resume",))
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.has_action("up"))

    def test_update_preserves_legacy_project_and_backup(self):
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / ".env").read_text(), self.original_env)
        backup = next((self.root / "backups").iterdir())
        self.assertEqual((backup / ".env").read_text(), self.original_env)
        self.assertEqual((backup / "database.dump").read_text().strip(), "test-logical-backup")
        self.assertEqual((backup / ".env").stat().st_mode & 0o777, 0o600)
        self.assertEqual(backup.stat().st_mode & 0o777, 0o700)
        rollback = json.loads((backup / "rollback.yaml").read_text())
        self.assertNotIn("build", rollback["services"]["app"])
        self.assertEqual(rollback["volumes"]["audit-postgres"]["name"], "legacy-project_audit-postgres")
        calls = self.calls()
        build = next(i for i,c in enumerate(calls) if "compose" in c and "build" in c)
        stop = next(i for i,c in enumerate(calls) if "compose" in c and "stop" in c)
        dump = next(i for i,c in enumerate(calls) if "pg_dump" in c)
        up = next(i for i,c in enumerate(calls) if "compose" in c and "up" in c)
        self.assertLess(build, stop)
        self.assertLess(stop, dump)
        self.assertLess(dump, up)
        self.assertFalse(any("down" in c for c in calls))
        self.assertTrue(all("--no-deps" in c and c[-1] == "app" and "legacy-project" in c
                            for c in calls if "compose" in c and "up" in c))
        self.assertNotIn(KEY, result.stdout + result.stderr)

    def test_no_pull_and_exported_credentials_ignored(self):
        self.env.update(MASTER_KEY="exported-wrong-key", ADMIN_PASSWORD="wrong-password", POSTGRES_PASSWORD="wrong-db", COMPOSE_FILE="override.yaml")
        result = self.run_script(args=("--no-pull",))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any(c[0] == "git" and c[1] in ("fetch", "merge") for c in self.calls()))

    def test_preflight_failures_never_stop_the_app(self):
        for mode in ("dirty", "branch", "wrong-directory", "override", "unhealthy-old", "env-drift", "fetch-failure", "merge-failure", "target-drift", "build-failure"):
            with self.subTest(mode=mode):
                result = self.run_script(mode=mode)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.has_action("stop"))
                self.assertFalse(self.has_action("up"))
                (self.state / "calls").unlink()
                (self.state / "merged").unlink(missing_ok=True)

    def test_backup_failures_restart_original_without_recreating(self):
        for mode in ("dump-failure", "archive-failure"):
            with self.subTest(mode=mode):
                result = self.run_script(mode=mode)
                self.assertNotEqual(result.returncode, 0)
                self.assertTrue(self.has_action("stop"))
                self.assertIn(["docker", "start", "app-original"], self.calls())
                self.assertFalse(self.has_action("up"))
                (self.state / "calls").unlink()

    def test_deployment_failure_restores_image_not_database(self):
        for mode in ("up-failure", "unhealthy-new"):
            with self.subTest(mode=mode):
                result = self.run_script(mode=mode)
                self.assertNotEqual(result.returncode, 0)
                self.assertTrue((self.state / "rolled-back").exists())
                self.assertIn("不会自动覆盖数据库", result.stderr)
                self.assertFalse(any("pg_restore" in c and "--list" not in c for c in self.calls()))
                (self.state / "calls").unlink()

    def test_lock_and_invalid_arguments(self):
        (self.root / ".deploy.lock").mkdir()
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue((self.root / ".deploy.lock").exists())
        for script in ("install.sh", "update.sh"):
            result = self.run_script(script, args=("--unknown",))
            self.assertNotEqual(result.returncode, 0)
            result = self.run_script(script, args=("--help",))
            self.assertEqual(result.returncode, 0)
