# Code generated from agentacp/v1/session.proto. DO NOT EDIT.
from collections.abc import AsyncGenerator
from typing import Protocol, TypeVar

from google.protobuf.message import Message

from .v1 import session_pb2 as model


T = TypeVar("T", bound=Message)


class Runtime(Protocol):
    """Implement the portable session operations and propagate task cancellation."""

    async def unary(self, operation: str, request: Message, response_type: type[T], /) -> T: ...

    def stream(
        self, operation: str, request: Message, response_type: type[T], /
    ) -> AsyncGenerator[T, None]:
        """Release the subscription on completion, cancellation or aclose()."""
        ...


class Sessions:
    def __init__(self, runtime: Runtime) -> None:
        self._runtime = runtime

    async def open(self, request: model.OpenRequest) -> model.OpenResponse:
        return await self._runtime.unary("Open", request, model.OpenResponse)

    async def resume(self, request: model.ResumeRequest) -> model.ResumeResponse:
        return await self._runtime.unary("Resume", request, model.ResumeResponse)

    async def send(self, request: model.SendRequest) -> model.SendResponse:
        return await self._runtime.unary("Send", request, model.SendResponse)

    def observe(self, request: model.ObserveRequest) -> AsyncGenerator[model.Observation, None]:
        return self._runtime.stream("Observe", request, model.Observation)

    async def answer(self, request: model.AnswerRequest) -> model.AnswerResponse:
        return await self._runtime.unary("Answer", request, model.AnswerResponse)

    async def interrupt(self, request: model.InterruptRequest) -> model.InterruptResponse:
        return await self._runtime.unary("Interrupt", request, model.InterruptResponse)

    async def list_sessions(self, request: model.ListSessionsRequest) -> model.ListSessionsResponse:
        return await self._runtime.unary("ListSessions", request, model.ListSessionsResponse)

    async def read_history(self, request: model.ReadHistoryRequest) -> model.ReadHistoryResponse:
        return await self._runtime.unary("ReadHistory", request, model.ReadHistoryResponse)
