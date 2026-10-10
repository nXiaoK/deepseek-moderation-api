"""Use real local Git remotes; never deploy containers or install host timers."""
import os
from pathlib import Path
import pwd
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
GIT = shutil.which("git")

FAKE_UPDATE = '''#!/usr/bin/env bash
set -euo pipefail
printf 'attempt\\n' >> "$AUTO_TEST_STATE/attempts"
if [[ ${AUTO_TEST_FAIL:-} == before-pull ]]; then exit 7; fi
git merge --ff-only refs/auto-update/main
if [[ ${AUTO_TEST_FAIL:-} == after-pull ]]; then exit 8; fi
'''


@unittest.skipUnless(GIT, "Git is required")
class AutoUpdateTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="audit-auto-update-")
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name).resolve()
        self.writer = self.base / "writer"
        self.remote = self.base / "origin.git"
        self.root = self.base / 'checkout with spaces $%"'
        self.state = self.base / "state"
        self.bin = self.base / "bin"
        for path in (self.writer, self.state, self.bin):
            path.mkdir()
        self.env = dict(os.environ, AUTO_TEST_STATE=str(self.state),
                        GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                        GIT_AUTHOR_NAME="test", GIT_AUTHOR_EMAIL="test@example.invalid",
                        GIT_COMMITTER_NAME="test", GIT_COMMITTER_EMAIL="test@example.invalid",
                        PATH=str(self.bin) + os.pathsep + os.environ["PATH"])
        for key in ("AUTO_TEST_FAIL", "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"):
            self.env.pop(key, None)
        # macOS does not ship the Debian utilities. Preserve actual file-lock
        # behavior there and use native flock/timeout on Linux.
        if not shutil.which("flock"):
            self.write_tool("flock", '''#!/usr/bin/env python3
import fcntl, sys
try:
    fcntl.flock(int(sys.argv[-1]), fcntl.LOCK_EX | fcntl.LOCK_NB)
except BlockingIOError:
    sys.exit(1)
''')
        if not shutil.which("timeout"):
            self.write_tool("timeout", '#!/usr/bin/env bash\nshift\nexec "$@"\n')
        self.git(self.remote.parent, "init", "--bare", str(self.remote))
        self.git(self.writer, "init", "-b", "main")
        for name in ("auto-update.sh", "install-auto-update.sh"):
            shutil.copy2(ROOT / name, self.writer / name)
        (self.writer / "update.sh").write_text(FAKE_UPDATE)
        (self.writer / "update.sh").chmod(0o755)
        (self.writer / "compose.yaml").write_text("services: {}\n")
        (self.writer / ".gitignore").write_text(".env\n.deploy.lock/\n")
        self.git(self.writer, "add", ".")
        self.git(self.writer, "commit", "-m", "initial")
        self.git(self.writer, "remote", "add", "origin", str(self.remote))
        self.git(self.writer, "push", "origin", "main")
        self.git(self.base, "clone", "-b", "main", str(self.remote), str(self.root))
        (self.root / ".env").write_text("test configuration\n")

    def write_tool(self, name, text):
        path = self.bin / name
        path.write_text(text)
        path.chmod(0o755)

    def git(self, directory, *args):
        return subprocess.check_output([GIT, *args], cwd=directory, env=self.env,
                                       text=True, stderr=subprocess.PIPE).strip()

    def advance_remote(self):
        path = self.writer / "change"
        path.write_text(path.read_text() + "next\n" if path.exists() else "next\n")
        self.git(self.writer, "add", ".")
        self.git(self.writer, "commit", "-m", "new version")
        self.git(self.writer, "push", "origin", "main")
        return self.git(self.writer, "rev-parse", "HEAD")

    def run_check(self, *args):
        return subprocess.run(["bash", str(self.root / "auto-update.sh"), *args],
                              cwd=self.base, env=self.env, text=True,
                              capture_output=True, timeout=20)

    def attempts(self):
        path = self.state / "attempts"
        return len(path.read_text().splitlines()) if path.exists() else 0

    def pending(self):
        return self.root / ".git/audit-auto-update.pending"

    def test_no_change_skips_deployment_and_new_commit_updates_once(self):
        self.assertEqual(self.run_check().returncode, 0)
        self.assertEqual(self.attempts(), 0)
        # Fetching for the timer must not overwrite a manual update's fetch.
        fetch_head = self.root / ".git/FETCH_HEAD"
        fetch_head.write_text("manual fetch sentinel\n")
        revision = self.advance_remote()
        result = self.run_check()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.git(self.root, "rev-parse", "HEAD"), revision)
        self.assertEqual(fetch_head.read_text(), "manual fetch sentinel\n")
        self.assertEqual(self.attempts(), 1)
        self.assertFalse(self.pending().exists())
        self.assertEqual(self.git(self.root, "status", "--porcelain"), "")
        self.assertEqual(self.run_check().returncode, 0)
        self.assertEqual(self.attempts(), 1)

    def test_retry_after_failure_that_already_advanced_head(self):
        revision = self.advance_remote()
        self.env["AUTO_TEST_FAIL"] = "after-pull"
        result = self.run_check()
        self.assertEqual(result.returncode, 8, result.stderr)
        self.assertEqual(self.git(self.root, "rev-parse", "HEAD"), revision)
        self.assertTrue(self.pending().exists())
        self.env.pop("AUTO_TEST_FAIL")
        result = self.run_check()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.attempts(), 2)
        self.assertFalse(self.pending().exists())
        self.run_check()
        self.assertEqual(self.attempts(), 2)

    def test_failed_update_before_pull_keeps_head_and_retries(self):
        old = self.git(self.root, "rev-parse", "HEAD")
        self.advance_remote()
        self.env["AUTO_TEST_FAIL"] = "before-pull"
        self.assertEqual(self.run_check().returncode, 7)
        self.assertEqual(self.git(self.root, "rev-parse", "HEAD"), old)
        self.env.pop("AUTO_TEST_FAIL")
        self.assertEqual(self.run_check().returncode, 0)
        self.assertEqual(self.attempts(), 2)

    def test_dirty_worktree_wrong_branch_and_deploy_lock(self):
        self.advance_remote()
        (self.root / "local-file").write_text("keep my work\n")
        self.assertNotEqual(self.run_check().returncode, 0)
        self.assertEqual(self.attempts(), 0)
        (self.root / "local-file").unlink()
        self.git(self.root, "checkout", "-b", "feature")
        self.assertNotEqual(self.run_check().returncode, 0)
        self.git(self.root, "checkout", "main")
        self.git(self.root, "checkout", "--detach")
        self.assertNotEqual(self.run_check().returncode, 0)
        self.git(self.root, "checkout", "main")
        (self.root / ".deploy.lock").mkdir()
        self.assertEqual(self.run_check().returncode, 0)
        self.assertEqual(self.attempts(), 0)
        self.assertFalse(self.pending().exists())

    def test_local_ahead_and_divergent_history_are_not_deployed(self):
        (self.root / "local").write_text("local commit\n")
        self.git(self.root, "add", ".")
        self.git(self.root, "commit", "-m", "local change")
        self.assertNotEqual(self.run_check().returncode, 0)
        self.advance_remote()
        self.assertNotEqual(self.run_check().returncode, 0)
        self.assertEqual(self.attempts(), 0)

    def test_fetch_failure_never_deploys_and_preserves_pending_retry(self):
        self.pending().write_text("previous failed attempt\n")
        self.git(self.root, "remote", "set-url", "origin", str(self.base / "missing.git"))
        self.assertNotEqual(self.run_check().returncode, 0)
        self.assertEqual(self.attempts(), 0)
        self.assertTrue(self.pending().exists())

    def test_concurrent_checker_skips_while_file_lock_is_held(self):
        import fcntl
        self.advance_remote()
        with (self.root / ".git/audit-auto-update.lock").open("w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            result = self.run_check()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(self.attempts(), 0)
        self.assertEqual(self.run_check().returncode, 0)
        self.assertEqual(self.attempts(), 1)

    def test_missing_deployment_or_invalid_arguments_fail_without_deploying(self):
        self.assertNotEqual(self.run_check("--unknown").returncode, 0)
        self.assertEqual(self.run_check("--help").returncode, 0)
        (self.root / ".env").unlink()
        self.assertNotEqual(self.run_check().returncode, 0)
        self.assertEqual(self.attempts(), 0)

    def test_unit_preview_escapes_paths_and_uses_checkout_owner(self):
        result = subprocess.run(["bash", str(self.root / "install-auto-update.sh"), "--print"],
                                env=self.env, text=True, capture_output=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("User=" + pwd.getpwuid(os.stat(self.root).st_uid).pw_name, result.stdout)
        self.assertIn('checkout with spaces $$%%\\"/auto-update.sh"', result.stdout)
        self.assertIn('WorkingDirectory=' + str(self.root).replace("%", "%%") + '\n', result.stdout)
        self.assertIn("OnCalendar=hourly\nPersistent=true", result.stdout)
        self.assertIn("TimeoutStartSec=infinity", result.stdout)
        self.assertEqual(self.attempts(), 0)


if __name__ == "__main__":
    unittest.main()
