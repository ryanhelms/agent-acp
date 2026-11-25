#!/usr/bin/env python3
"""Build independent SDK packages from an agent-acp checkout."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
GO_MODULE = "agent-acp.local/sdks/go"


def run(args, cwd, env=None):
    subprocess.run(args, cwd=cwd, env=env, check=True)


def stage_checkout(destination):
    tracked = subprocess.check_output(["git", "ls-files", "-z"], cwd=ROOT)
    snapshot = hashlib.sha256()
    for item in tracked.split(b"\x00"):
        if not item:
            continue
        path = Path(os.fsdecode(item))
        source = ROOT / path
        target = destination / path
        if source.is_symlink() or not source.is_file():
            raise ValueError(f"Expected a regular checkout file: {path}")
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, target)
        snapshot.update(item + b"\x00" + hashlib.sha256(target.read_bytes()).digest())
    return snapshot.hexdigest()


def go_package(stage, output, version, timestamp):
    source = stage / "sdks/go"
    fixture = source / "testdata/session.json"
    fixture.parent.mkdir(exist_ok=True)
    shutil.copyfile(stage / "conformance/session.json", fixture)
    directory = output / "go" / GO_MODULE / "@v"
    directory.mkdir(parents=True)
    tag = "v" + version
    shutil.copyfile(source / "go.mod", directory / (tag + ".mod"))
    (directory / (tag + ".info")).write_text(
        json.dumps({"Version": tag, "Time": timestamp.isoformat().replace("+00:00", "Z")}) + "\n",
        encoding="utf-8",
    )
    (directory / "list").write_text(tag + "\n", encoding="utf-8")
    with zipfile.ZipFile(directory / (tag + ".zip"), "w") as archive:
        for path in sorted(source.rglob("*")):
            if path.is_file():
                member = zipfile.ZipInfo(f"{GO_MODULE}@{tag}/{path.relative_to(source).as_posix()}")
                member.external_attr = 0o100644 << 16
                archive.writestr(member, path.read_bytes(), compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)


def npm_package(stage, output, version, env):
    destination = output / "npm"
    destination.mkdir()
    run(["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"], stage, env)
    run(["npm", "run", "build", "--workspace", "sdks/typescript"], stage, env)
    source = stage / "sdks/typescript"
    manifest = source / "package.json"
    package = json.loads(manifest.read_text(encoding="utf-8"))
    package["version"] = version
    manifest.write_text(json.dumps(package, indent=2) + "\n", encoding="utf-8")
    run(["npm", "pack", "--ignore-scripts", "--pack-destination", destination], source, env)


def python_package(stage, output, version, env):
    destination = output / "python"
    destination.mkdir()
    source = stage / "sdks/python"
    manifest = source / "pyproject.toml"
    text, changed = re.subn(r'^version = "[^"]+"$', f'version = "{version}"',
                            manifest.read_text(encoding="utf-8"), flags=re.MULTILINE)
    if changed != 1:
        raise ValueError("Expected one Python project version")
    manifest.write_text(text, encoding="utf-8")
    run([sys.executable, "-m", "pip", "wheel", "--no-deps", "--no-build-isolation",
         "--wheel-dir", destination, "."], source, env)


def manifest(output, version, timestamp, revision, snapshot):
    files = {
        path.relative_to(output).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
        for path in sorted(output.rglob("*")) if path.is_file()
    }
    (output / "manifest.json").write_text(json.dumps({
        "name": "agent-acp", "version": version, "goModule": GO_MODULE,
        "sourceDateEpoch": int(timestamp.timestamp()), "sourceRevision": revision,
        "sourceSnapshotSHA256": snapshot, "sha256": files,
    }, indent=2) + "\n", encoding="utf-8")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version", help="release version, for example 0.1.0")
    parser.add_argument("output", type=Path, help="new directory for the release")
    args = parser.parse_args()
    if not re.fullmatch(r"(?:0|1)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", args.version):
        parser.error("provide a major-0 or major-1 release version for the existing Go module path")
    output = args.output.resolve()
    if output.exists():
        parser.error("the release directory already exists")
    epoch = os.environ.get("SOURCE_DATE_EPOCH")
    if epoch is None:
        epoch = subprocess.check_output(["git", "log", "-1", "--format=%ct"], cwd=ROOT, text=True).strip()
    timestamp = datetime.datetime.fromtimestamp(int(epoch), datetime.timezone.utc)
    env = os.environ | {"SOURCE_DATE_EPOCH": epoch, "PYTHONDONTWRITEBYTECODE": "1"}
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".agent-acp-release-", dir=output.parent) as scratch:
        work = Path(scratch)
        stage = work / "source"
        release = work / "release"
        release.mkdir()
        revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        snapshot = stage_checkout(stage)
        go_package(stage, release, args.version, timestamp)
        npm_package(stage, release, args.version, env)
        python_package(stage, release, args.version, env)
        shutil.copytree(stage / "conformance", release / "conformance")
        manifest(release, args.version, timestamp, revision, snapshot)
        release.rename(output)
    print(output)


if __name__ == "__main__":
    main()
