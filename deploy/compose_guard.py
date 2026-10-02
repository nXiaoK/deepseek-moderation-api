#!/usr/bin/env python3
"""Fail-closed checks for the repository's standard Compose deployment.

Never source .env as shell code and never include credential values in errors.
Only the Python standard library and Docker CLI are required.
"""
import base64
import copy
import json
import os
import re
import subprocess
import sys


class GuardError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise GuardError(message)


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


def inspect(container):
    return json.loads(docker("inspect", container))[0]


def env_map(container):
    return dict(item.split("=", 1) for item in container["Config"].get("Env", []) if "=" in item)


def literal_env(value):
    # `compose config` escapes literal dollars for reusable YAML/JSON output.
    # Container inspect reports the unescaped runtime value instead.
    return str(value).replace("$$", "$")


def volume_name(config):
    mounts = config["services"]["db"].get("volumes", [])
    require(len(mounts) == 1 and mounts[0].get("type") == "volume"
            and mounts[0].get("target") == "/var/lib/postgresql/data",
            "仅支持标准 PostgreSQL 命名卷；其他挂载请手动更新")
    name = config.get("volumes", {}).get(mounts[0]["source"], {}).get("name")
    require(bool(name), "无法确定 PostgreSQL 数据卷名称")
    return name


def credentials(config):
    services = config.get("services", {})
    require(set(services) == {"app", "db"}, "仅支持仓库标准 app/db 编排")
    app = services["app"].get("environment", {})
    db = services["db"].get("environment", {})
    try:
        key = base64.b64decode(app.get("MASTER_KEY", ""), validate=True)
    except (ValueError, TypeError):
        raise GuardError("MASTER_KEY 必须是 Base64 编码的 32 字节密钥") from None
    require(len(key) == 32, "MASTER_KEY 必须是 Base64 编码的 32 字节密钥")
    require(bool(app.get("ADMIN_USER")) and bool(app.get("ADMIN_PASSWORD")), "管理员配置不完整")
    password = db.get("POSTGRES_PASSWORD", "")
    require(isinstance(password, str) and re.fullmatch(r"[A-Za-z0-9_.~-]+", password),
            "POSTGRES_PASSWORD 需要非空 URI 安全字符；特殊字符请按手动部署文档配置")
    require(db.get("POSTGRES_USER") == "audit" and db.get("POSTGRES_DB") == "audit",
            "仅支持标准 audit 数据库和账号")
    require(app.get("DATABASE_URL") == f"postgres://audit:{password}@db:5432/audit?sslmode=disable",
            "应用数据库地址与标准 db 服务不一致")
    for service in services.values():
        require(not any(service.get(key) for key in ("env_file", "secrets", "configs")),
                "外部环境文件、secrets 或 configs 请手动备份更新")
    require(bool(services["app"].get("build")), "仅支持源码构建的 app 服务；预构建镜像请手动更新")
    require(not services["app"].get("volumes"), "应用存在额外挂载，请手动更新")
    volume_name(config)


def labels_match(container, root, project=None):
    labels = container.get("Config", {}).get("Labels", {}) or {}
    directory = labels.get("com.docker.compose.project.working_dir", "")
    return (bool(directory) and os.path.realpath(directory) == root
            and (project is None or labels.get("com.docker.compose.project") == project))


def check_labels(container, root, project):
    require(labels_match(container, root, project), "容器不属于原部署目录和 Compose 项目")
    labels = container["Config"]["Labels"]
    files = labels.get("com.docker.compose.project.config_files", "").split(",")
    require(len(files) == 1 and os.path.realpath(files[0]) == os.path.join(root, "compose.yaml"),
            "存在额外 Compose 覆盖文件或回退配置，请按 UPDATING.md 手动更新")


def discover(root):
    ids = docker("ps", "--no-trunc", "-aq", "--filter", "label=com.docker.compose.service=app").split()
    apps = [inspect(cid) for cid in ids]
    apps = [item for item in apps if labels_match(item, root)]
    require(len(apps) == 1, "原目录必须有且仅有一个 app 容器；不要从新克隆目录更新")
    app = apps[0]
    project = app["Config"]["Labels"]["com.docker.compose.project"]
    require(re.fullmatch(r"[a-z0-9][a-z0-9_-]*", project), "原 Compose 项目名无效")
    require(not app["Config"]["Image"].startswith("deepseek-audit-backup:"),
            "当前使用回退镜像，请先按 UPDATING.md 核对源码和迁移兼容性")
    ids = docker("ps", "--no-trunc", "-aq", "--filter", "label=com.docker.compose.service=db",
                 "--filter", f"label=com.docker.compose.project={project}").split()
    require(len(ids) == 1, "无法唯一识别原 PostgreSQL 容器")
    db = inspect(ids[0])
    for item in (app, db):
        check_labels(item, root, project)
        state = item.get("State", {})
        require(state.get("Status") == "running" and state.get("Health", {}).get("Status") == "healthy",
                "原应用或数据库未运行/未通过健康检查，先排查当前部署")
    mounts = db.get("Mounts", [])
    require(len(mounts) == 1 and mounts[0].get("Type") == "volume"
            and mounts[0].get("Destination") == "/var/lib/postgresql/data" and mounts[0].get("Name"),
            "原数据库不是标准单一命名卷，请手动更新")
    return project, app["Id"], db["Id"], mounts[0]["Name"]


def check_ports(service, container):
    desired = {}
    for port in service.get("ports", []):
        require(isinstance(port, dict), "无法识别端口配置，请手动更新")
        key = f"{port['target']}/{port.get('protocol', 'tcp')}"
        desired.setdefault(key, []).append({"HostIp": port.get("host_ip", ""),
                                            "HostPort": str(port.get("published", ""))})
    actual = container.get("HostConfig", {}).get("PortBindings") or {}
    require(desired == actual, "当前 Compose 端口与运行容器不一致，请手动核对")


def check_backup_path(root, backup, db):
    path = os.path.realpath(backup)
    require(path != root, "备份目录不能是源码根目录")
    for mount in db.get("Mounts", []):
        source = mount.get("Source")
        require(bool(source), "无法识别原数据卷的宿主机路径")
        source = os.path.realpath(source)
        require(os.path.commonpath((source, path)) != source,
                "备份目录不能位于数据库数据目录内部")


def check_current(config, app, db, name):
    credentials(config)
    require(volume_name(config) == name, "当前 Compose 会切换数据库卷，拒绝更新")
    require(not app.get("Mounts"), "原应用存在额外挂载，请手动更新")
    for service, actual in (("app", app), ("db", db)):
        expected = config["services"][service]
        check_ports(expected, actual)
        actual_env = env_map(actual)
        changed = [key for key, value in expected.get("environment", {}).items()
                   if literal_env(value) != actual_env.get(key)]
        require(not changed, f"{service} 容器与 .env/Compose 的环境不一致：" + ", ".join(changed))
    require(config["services"]["db"]["image"] == db["Config"]["Image"],
            "PostgreSQL 镜像配置已改变，请手动更新")


def check_target(old, new):
    credentials(new)
    before, after = copy.deepcopy(old), copy.deepcopy(new)
    old_env = before["services"]["app"].pop("environment", {})
    new_env = after["services"]["app"].pop("environment", {})
    changed = [key for key, value in old_env.items() if new_env.get(key) != value]
    require(not changed, "新代码将改变既有环境变量：" + ", ".join(changed))
    require(all(key.startswith("AUDIT_") for key in new_env.keys() - old_env.keys()),
            "新代码增加了非 AUDIT_ 环境变量，请手动核对")
    for config in (before, after):
        for key in ("build", "image"):
            config["services"]["app"].pop(key, None)
    require(before == after, "新 Compose 改变数据库、端口、挂载或其他部署设置，请手动核对；原应用未停止")


def check_install(root, project, resume):
    ids = docker("ps", "--no-trunc", "-aq").split()
    for cid in ids:
        item = inspect(cid)
        labels = item.get("Config", {}).get("Labels", {}) or {}
        same_project = labels.get("com.docker.compose.project") == project
        same_dir = labels_match(item, root)
        if same_project or same_dir:
            require(resume and same_project and same_dir, "已有部署容器，不能生成新凭据；请在原目录运行更新脚本")
            check_labels(item, root, project)
    volumes = docker("volume", "ls", "-q", "--filter", "label=com.docker.compose.volume=audit-postgres").split()
    # Also find the default-named volume if its Compose labels are missing.
    if subprocess.run(["docker", "volume", "inspect", f"{project}_audit-postgres"],
                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
        volumes.append(f"{project}_audit-postgres")
    for name in set(volumes):
        require(resume and name == f"{project}_audit-postgres",
                "已有 PostgreSQL 数据卷，不能生成新主密钥或接管其他部署")
        item = json.loads(docker("volume", "inspect", name))[0]
        labels = item.get("Labels") or {}
        require(labels.get("com.docker.compose.project") == project
                and labels.get("com.docker.compose.volume") == "audit-postgres",
                "已有数据卷的 Compose 标签不匹配，不能继续安装")


def check_resume(config, root, project):
    credentials(config)
    ids = docker("ps", "--no-trunc", "-aq", "--filter", f"label=com.docker.compose.project={project}").split()
    for cid in ids:
        item = inspect(cid)
        check_labels(item, root, project)
        service = item["Config"]["Labels"].get("com.docker.compose.service")
        require(service in config["services"], "存在非标准服务容器，请手动处理")
        expected = config["services"][service].get("environment", {})
        actual = env_map(item)
        require(all(literal_env(value) == actual.get(key) for key, value in expected.items()),
                "现有容器与 .env 环境不一致；不能用 --resume 替换凭据")
        if service == "db":
            mounts = item.get("Mounts", [])
            require(len(mounts) == 1 and mounts[0].get("Name") == volume_name(config),
                    "现有数据库卷与配置不一致，不能继续安装")


def main(argv):
    command, *args = argv
    if command == "install":
        check_install(args[0], args[1], args[2] == "1")
    elif command == "credentials":
        credentials(json.load(sys.stdin))
    elif command == "resume":
        check_resume(json.load(sys.stdin), args[0], args[1])
    elif command == "discover":
        print("\n".join(discover(args[0])))
    elif command == "backup-path":
        check_backup_path(args[0], args[1], inspect(args[2]))
    elif command == "current":
        with open(args[0]) as source:
            check_current(json.load(source), inspect(args[1]), inspect(args[2]), args[3])
    elif command == "target":
        with open(args[0]) as a, open(args[1]) as b:
            check_target(json.load(a), json.load(b))
    elif command == "rollback":
        with open(args[0]) as source:
            config = json.load(source)
        app = config["services"]["app"]
        app.pop("build", None)
        app["image"] = args[1]
        app["pull_policy"] = "never"
        # JSON is valid YAML, avoiding an extra PyYAML dependency.
        print(json.dumps(config, indent=2))
    else:
        raise GuardError("未知部署检查命令")


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except (GuardError, subprocess.CalledProcessError, OSError, ValueError, KeyError, TypeError, IndexError) as error:
        # Config parsing errors may include secrets: only GuardError is safe to print.
        message = str(error) if isinstance(error, GuardError) else "部署配置读取失败，请手动核对"
        print("部署安全检查失败：" + message, file=sys.stderr)
        sys.exit(1)
