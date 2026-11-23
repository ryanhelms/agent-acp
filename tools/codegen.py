"""Build the SDK vocabulary and client facades from the portable session schema."""

import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import venv


ROOT = Path(__file__).resolve().parents[1]
TOOLS = ROOT / ".tools"
PYTHON = TOOLS / "venv" / "bin" / "python"
NODE_BIN = ROOT / "node_modules" / ".bin"
GO_PLUGIN = TOOLS / "bin" / "protoc-gen-go"
GO_VERSION = "v1.36.12"
PYTHON_VERSION = "7.36.2"
# Each compiler writes into a temporary SDK tree. The globs identify obsolete
# compiler outputs when a schema file is renamed or removed.
TARGETS = {
    "go": (
        "sdks/go",
        [f"--plugin=protoc-gen-go={GO_PLUGIN}", "--go_opt=module=agent-acp.local/sdks/go"],
        ["go"],
        ["model/**/*.pb.go"],
    ),
    "typescript": (
        "sdks/typescript/src/gen",
        [f"--plugin=protoc-gen-es={NODE_BIN / 'protoc-gen-es'}", "--es_opt=target=ts,import_extension=js"],
        ["es"],
        ["**/*_pb.ts"],
    ),
    "python": (
        "sdks/python",
        [],
        ["python", "pyi"],
        ["agentacp/**/*_pb2.py", "agentacp/**/*_pb2.pyi"],
    ),
}


def setup():
    if not (NODE_BIN / "protoc").is_file():
        sys.exit("Install the locked Node dependencies with npm ci first.")
    venv.EnvBuilder(with_pip=True).create(TOOLS / "venv")
    subprocess.run([PYTHON, "-m", "pip", "install", f"protobuf=={PYTHON_VERSION}"], check=True)
    GO_PLUGIN.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(
        ["go", "install", f"google.golang.org/protobuf/cmd/protoc-gen-go@{GO_VERSION}"],
        cwd=ROOT,
        env={**os.environ, "GOBIN": str(GO_PLUGIN.parent)},
        check=True,
    )


def generate(out, languages):
    schema = ROOT / "schema"
    sources = sorted(path.relative_to(schema) for path in schema.rglob("*.proto"))
    descriptor = out / "schema.pb"
    command = [NODE_BIN / "protoc", "-I", schema, f"--descriptor_set_out={descriptor}", "--include_imports"]
    for language in languages:
        directory, flags, plugins, _ = TARGETS[language]
        destination = out / directory
        destination.mkdir(parents=True, exist_ok=True)
        command.extend(flags)
        command.extend(f"--{plugin}_out={destination}" for plugin in plugins)
    subprocess.run([*command, *sources], cwd=schema, check=True)
    facades = [PYTHON, ROOT / "tools" / "facades.py", "--descriptor", descriptor, "--out", out]
    for language in languages:
        facades.extend(["--language", language])
    subprocess.run(facades, cwd=ROOT, check=True)


def sync(out, languages, check):
    expected = {path.relative_to(out) for path in (out / "sdks").rglob("*") if path.is_file()}
    obsolete = set()
    for language in languages:
        directory, _, _, patterns = TARGETS[language]
        for pattern in patterns:
            obsolete.update(p.relative_to(ROOT) for p in (ROOT / directory).glob(pattern) if p.is_file())
    obsolete -= expected
    changed = set()
    for name in sorted(expected):
        output = ROOT / name
        if output.is_file() and output.read_bytes() == (out / name).read_bytes():
            continue
        changed.add(name)
        if not check:
            output.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(out / name, output)
    if not check:
        for name in obsolete:
            (ROOT / name).unlink()
    differences = sorted(changed | obsolete)
    if differences:
        print("\n".join(str(name) for name in differences))
    if check and differences:
        sys.exit("Generated SDK files differ. Run npm run generate.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["setup", "generate", "check"])
    parser.add_argument("--language", choices=TARGETS, action="append", help="limit to a language (repeatable)")
    args = parser.parse_args()
    if args.action == "setup":
        setup()
        return
    languages = list(dict.fromkeys(args.language or TARGETS))
    required = [PYTHON, NODE_BIN / "protoc"]
    if "go" in languages:
        required.append(GO_PLUGIN)
    if any(not path.is_file() for path in required):
        sys.exit("Prepare local generators with npm ci and npm run setup.")
    with tempfile.TemporaryDirectory(prefix="agent-acp-generate-") as directory:
        out = Path(directory)
        generate(out, languages)
        sync(out, languages, args.action == "check")


if __name__ == "__main__":
    main()
