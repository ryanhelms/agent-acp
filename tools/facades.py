"""Generate the in-process session clients from the compiled service descriptor."""

import argparse
import re
from dataclasses import dataclass
from pathlib import Path
from string import Template

from google.protobuf.descriptor_pb2 import FileDescriptorSet


TEMPLATES = Path(__file__).with_name("facades")
SESSION_SERVICE = "agentacp.v1.Sessions"


@dataclass(frozen=True)
class Method:
    name: str
    request: str
    response: str
    stream: bool


def session_service(descriptor):
    for file in descriptor.file:
        for service in file.service:
            if f"{file.package}.{service.name}" != SESSION_SERVICE:
                continue
            local_types = {f".{file.package}.{m.name}": m.name for m in file.message_type}
            methods = []
            for method in service.method:
                if method.client_streaming:
                    raise ValueError(f"{method.name}: client streaming requires a runtime hook")
                try:
                    request = local_types[method.input_type]
                    response = local_types[method.output_type]
                except KeyError as exc:
                    raise ValueError(f"{method.name}: expected a top-level message from {file.name}: {exc}") from exc
                methods.append(Method(method.name, request, response, method.server_streaming))
            return file, methods
    raise ValueError(f"missing service descriptor for {SESSION_SERVICE}")


def render(language, file, methods, **paths):
    template = Template((TEMPLATES / f"{language}.tmpl").read_text(encoding="utf-8"))
    return template.substitute(source=file.name, methods="\n\n".join(methods), **paths)


def go(file, methods):
    blocks = []
    for m in methods:
        arguments = f"ctx context.Context, request *model.{m.request}"
        response = f"*model.{m.response}"
        if m.stream:
            result = f"iter.Seq2[{response}, error]"
            call = f'stream(ctx, s.runtime, "{m.name}", request, func() {response} {{ return new(model.{m.response}) }})'
        else:
            result = f"({response}, error)"
            call = f'unary(ctx, s.runtime, "{m.name}", request, new(model.{m.response}))'
        blocks.append(f"func (s *Sessions) {m.name}({arguments}) {result} {{\n\treturn {call}\n}}")
    model_import = file.options.go_package.split(";")[0]
    if not model_import:
        raise ValueError(f"{file.name} requires option go_package")
    return {"sdks/go/session.go": render("go", file, blocks, model_import=model_import)}


def typescript(file, methods):
    blocks = []
    for m in methods:
        name = m.name[0].lower() + m.name[1:]
        hook = "stream" if m.stream else "unary"
        wrapper = "AsyncIterable" if m.stream else "Promise"
        blocks.append(
            f"  {name}(request: model.{m.request}, signal?: AbortSignal): {wrapper}<model.{m.response}> {{\n"
            f'    return this.runtime.{hook}("{m.name}", request, model.{m.response}Schema, signal);\n'
            f"  }}"
        )
    module = "./gen/" + str(Path(file.name).with_suffix("")) + "_pb.js"
    return {"sdks/typescript/src/index.ts": render("typescript", file, blocks, model_import=module)}


def snake_case(name):
    words = re.sub(r"(.)([A-Z][a-z]+)", r"\1_\2", name)
    return re.sub(r"([a-z0-9])([A-Z])", r"\1_\2", words).lower()


def python(file, methods):
    blocks = []
    for m in methods:
        if m.stream:
            declaration, result, call = "def", f"AsyncGenerator[model.{m.response}, None]", "self._runtime.stream"
        else:
            declaration, result, call = "async def", f"model.{m.response}", "await self._runtime.unary"
        blocks.append(
            f"    {declaration} {snake_case(m.name)}(self, request: model.{m.request}) -> {result}:\n"
            f'        return {call}("{m.name}", request, model.{m.response})'
        )
    proto_path = Path(file.name)
    module = proto_path.stem + "_pb2"
    model_package = "." + ".".join(proto_path.parent.parts[1:])
    api = render("python", file, blocks, model_package=model_package, model_name=module)
    exports = f"""# Code generated from {file.name}. DO NOT EDIT.
from {model_package} import {module} as model
from .session import Runtime, Sessions

__all__ = ["Runtime", "Sessions", "model"]
"""
    return {"sdks/python/agentacp/session.py": api, "sdks/python/agentacp/__init__.py": exports}


TARGETS = {"go": go, "typescript": typescript, "python": python}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--descriptor", required=True, type=Path)
    parser.add_argument("--out", required=True, type=Path)
    parser.add_argument("--language", action="append", choices=TARGETS)
    args = parser.parse_args()
    descriptor = FileDescriptorSet.FromString(args.descriptor.read_bytes())
    file, methods = session_service(descriptor)
    for language in args.language or TARGETS:
        for relative_path, content in TARGETS[language](file, methods).items():
            path = args.out / relative_path
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding="utf-8", newline="\n")


if __name__ == "__main__":
    main()
