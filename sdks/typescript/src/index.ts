// Code generated from agentacp/v1/session.proto. DO NOT EDIT.

import type { DescMessage, Message, MessageShape } from "@bufbuild/protobuf";
import * as model from "./gen/agentacp/v1/session_pb.js";

export * from "./gen/agentacp/v1/session_pb.js";

/** Adapters implement portable session operations and honor caller cancellation. */
export interface Runtime {
  unary<O extends DescMessage>(
    operation: string,
    request: Message,
    output: O,
    signal?: AbortSignal,
  ): Promise<MessageShape<O>>;

  /** The iterator releases its subscription on return, completion or failure. */
  stream<O extends DescMessage>(
    operation: string,
    request: Message,
    output: O,
    signal?: AbortSignal,
  ): AsyncIterable<MessageShape<O>>;
}

export class Sessions {
  constructor(private readonly runtime: Runtime) {}

  open(request: model.OpenRequest, signal?: AbortSignal): Promise<model.OpenResponse> {
    return this.runtime.unary("Open", request, model.OpenResponseSchema, signal);
  }

  resume(request: model.ResumeRequest, signal?: AbortSignal): Promise<model.ResumeResponse> {
    return this.runtime.unary("Resume", request, model.ResumeResponseSchema, signal);
  }

  send(request: model.SendRequest, signal?: AbortSignal): Promise<model.SendResponse> {
    return this.runtime.unary("Send", request, model.SendResponseSchema, signal);
  }

  observe(request: model.ObserveRequest, signal?: AbortSignal): AsyncIterable<model.Observation> {
    return this.runtime.stream("Observe", request, model.ObservationSchema, signal);
  }

  answer(request: model.AnswerRequest, signal?: AbortSignal): Promise<model.AnswerResponse> {
    return this.runtime.unary("Answer", request, model.AnswerResponseSchema, signal);
  }

  interrupt(request: model.InterruptRequest, signal?: AbortSignal): Promise<model.InterruptResponse> {
    return this.runtime.unary("Interrupt", request, model.InterruptResponseSchema, signal);
  }

  listSessions(request: model.ListSessionsRequest, signal?: AbortSignal): Promise<model.ListSessionsResponse> {
    return this.runtime.unary("ListSessions", request, model.ListSessionsResponseSchema, signal);
  }

  readHistory(request: model.ReadHistoryRequest, signal?: AbortSignal): Promise<model.ReadHistoryResponse> {
    return this.runtime.unary("ReadHistory", request, model.ReadHistoryResponseSchema, signal);
  }
}
