from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class DeliveryStatus(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    DELIVERY_STATUS_UNSPECIFIED: _ClassVar[DeliveryStatus]
    DELIVERY_STATUS_ACCEPTED: _ClassVar[DeliveryStatus]
    DELIVERY_STATUS_DELIVERED: _ClassVar[DeliveryStatus]
    DELIVERY_STATUS_UNKNOWN: _ClassVar[DeliveryStatus]
    DELIVERY_STATUS_UNSUPPORTED: _ClassVar[DeliveryStatus]

class Speaker(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    SPEAKER_UNSPECIFIED: _ClassVar[Speaker]
    SPEAKER_USER: _ClassVar[Speaker]
    SPEAKER_ASSISTANT: _ClassVar[Speaker]
    SPEAKER_SYSTEM: _ClassVar[Speaker]
DELIVERY_STATUS_UNSPECIFIED: DeliveryStatus
DELIVERY_STATUS_ACCEPTED: DeliveryStatus
DELIVERY_STATUS_DELIVERED: DeliveryStatus
DELIVERY_STATUS_UNKNOWN: DeliveryStatus
DELIVERY_STATUS_UNSUPPORTED: DeliveryStatus
SPEAKER_UNSPECIFIED: Speaker
SPEAKER_USER: Speaker
SPEAKER_ASSISTANT: Speaker
SPEAKER_SYSTEM: Speaker

class SessionRef(_message.Message):
    __slots__ = ("harness_instance", "session_id")
    HARNESS_INSTANCE_FIELD_NUMBER: _ClassVar[int]
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    harness_instance: str
    session_id: str
    def __init__(self, harness_instance: _Optional[str] = ..., session_id: _Optional[str] = ...) -> None: ...

class Capabilities(_message.Message):
    __slots__ = ("can_resume", "can_answer", "can_interrupt", "can_list_sessions", "can_read_history", "can_prompt_image", "can_prompt_audio")
    CAN_RESUME_FIELD_NUMBER: _ClassVar[int]
    CAN_ANSWER_FIELD_NUMBER: _ClassVar[int]
    CAN_INTERRUPT_FIELD_NUMBER: _ClassVar[int]
    CAN_LIST_SESSIONS_FIELD_NUMBER: _ClassVar[int]
    CAN_READ_HISTORY_FIELD_NUMBER: _ClassVar[int]
    CAN_PROMPT_IMAGE_FIELD_NUMBER: _ClassVar[int]
    CAN_PROMPT_AUDIO_FIELD_NUMBER: _ClassVar[int]
    can_resume: bool
    can_answer: bool
    can_interrupt: bool
    can_list_sessions: bool
    can_read_history: bool
    can_prompt_image: bool
    can_prompt_audio: bool
    def __init__(self, can_resume: _Optional[bool] = ..., can_answer: _Optional[bool] = ..., can_interrupt: _Optional[bool] = ..., can_list_sessions: _Optional[bool] = ..., can_read_history: _Optional[bool] = ..., can_prompt_image: _Optional[bool] = ..., can_prompt_audio: _Optional[bool] = ...) -> None: ...

class Session(_message.Message):
    __slots__ = ("ref", "capabilities", "display_name")
    REF_FIELD_NUMBER: _ClassVar[int]
    CAPABILITIES_FIELD_NUMBER: _ClassVar[int]
    DISPLAY_NAME_FIELD_NUMBER: _ClassVar[int]
    ref: SessionRef
    capabilities: Capabilities
    display_name: str
    def __init__(self, ref: _Optional[_Union[SessionRef, _Mapping]] = ..., capabilities: _Optional[_Union[Capabilities, _Mapping]] = ..., display_name: _Optional[str] = ...) -> None: ...

class OpenRequest(_message.Message):
    __slots__ = ("harness_instance", "working_directory", "model")
    HARNESS_INSTANCE_FIELD_NUMBER: _ClassVar[int]
    WORKING_DIRECTORY_FIELD_NUMBER: _ClassVar[int]
    MODEL_FIELD_NUMBER: _ClassVar[int]
    harness_instance: str
    working_directory: str
    model: str
    def __init__(self, harness_instance: _Optional[str] = ..., working_directory: _Optional[str] = ..., model: _Optional[str] = ...) -> None: ...

class OpenResponse(_message.Message):
    __slots__ = ("session",)
    SESSION_FIELD_NUMBER: _ClassVar[int]
    session: Session
    def __init__(self, session: _Optional[_Union[Session, _Mapping]] = ...) -> None: ...

class ResumeRequest(_message.Message):
    __slots__ = ("session",)
    SESSION_FIELD_NUMBER: _ClassVar[int]
    session: SessionRef
    def __init__(self, session: _Optional[_Union[SessionRef, _Mapping]] = ...) -> None: ...

class ResumeResponse(_message.Message):
    __slots__ = ("session",)
    SESSION_FIELD_NUMBER: _ClassVar[int]
    session: Session
    def __init__(self, session: _Optional[_Union[Session, _Mapping]] = ...) -> None: ...

class Receipt(_message.Message):
    __slots__ = ("operation_id", "status", "detail")
    OPERATION_ID_FIELD_NUMBER: _ClassVar[int]
    STATUS_FIELD_NUMBER: _ClassVar[int]
    DETAIL_FIELD_NUMBER: _ClassVar[int]
    operation_id: str
    status: DeliveryStatus
    detail: str
    def __init__(self, operation_id: _Optional[str] = ..., status: _Optional[_Union[DeliveryStatus, str]] = ..., detail: _Optional[str] = ...) -> None: ...

class SendRequest(_message.Message):
    __slots__ = ("session", "operation_id", "text", "parts")
    SESSION_FIELD_NUMBER: _ClassVar[int]
    OPERATION_ID_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    PARTS_FIELD_NUMBER: _ClassVar[int]
    session: SessionRef
    operation_id: str
    text: str
    parts: _containers.RepeatedCompositeFieldContainer[PromptPart]
    def __init__(self, session: _Optional[_Union[SessionRef, _Mapping]] = ..., operation_id: _Optional[str] = ..., text: _Optional[str] = ..., parts: _Optional[_Iterable[_Union[PromptPart, _Mapping]]] = ...) -> None: ...

class PromptPart(_message.Message):
    __slots__ = ("text", "image", "audio")
    TEXT_FIELD_NUMBER: _ClassVar[int]
    IMAGE_FIELD_NUMBER: _ClassVar[int]
    AUDIO_FIELD_NUMBER: _ClassVar[int]
    text: str
    image: PromptMedia
    audio: PromptMedia
    def __init__(self, text: _Optional[str] = ..., image: _Optional[_Union[PromptMedia, _Mapping]] = ..., audio: _Optional[_Union[PromptMedia, _Mapping]] = ...) -> None: ...

class PromptMedia(_message.Message):
    __slots__ = ("mime_type", "data")
    MIME_TYPE_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    mime_type: str
    data: bytes
    def __init__(self, mime_type: _Optional[str] = ..., data: _Optional[bytes] = ...) -> None: ...

class SendResponse(_message.Message):
    __slots__ = ("receipt",)
    RECEIPT_FIELD_NUMBER: _ClassVar[int]
    receipt: Receipt
    def __init__(self, receipt: _Optional[_Union[Receipt, _Mapping]] = ...) -> None: ...

class AnswerRequest(_message.Message):
    __slots__ = ("session", "operation_id", "request_id", "text", "option_id")
    SESSION_FIELD_NUMBER: _ClassVar[int]
    OPERATION_ID_FIELD_NUMBER: _ClassVar[int]
    REQUEST_ID_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    OPTION_ID_FIELD_NUMBER: _ClassVar[int]
    session: SessionRef
    operation_id: str
    request_id: str
    text: str
    option_id: str
    def __init__(self, session: _Optional[_Union[SessionRef, _Mapping]] = ..., operation_id: _Optional[str] = ..., request_id: _Optional[str] = ..., text: _Optional[str] = ..., option_id: _Optional[str] = ...) -> None: ...

class AnswerResponse(_message.Message):
    __slots__ = ("receipt",)
    RECEIPT_FIELD_NUMBER: _ClassVar[int]
    receipt: Receipt
    def __init__(self, receipt: _Optional[_Union[Receipt, _Mapping]] = ...) -> None: ...

class InterruptRequest(_message.Message):
    __slots__ = ("session", "operation_id", "turn_id")
    SESSION_FIELD_NUMBER: _ClassVar[int]
    OPERATION_ID_FIELD_NUMBER: _ClassVar[int]
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    session: SessionRef
    operation_id: str
    turn_id: str
    def __init__(self, session: _Optional[_Union[SessionRef, _Mapping]] = ..., operation_id: _Optional[str] = ..., turn_id: _Optional[str] = ...) -> None: ...

class InterruptResponse(_message.Message):
    __slots__ = ("receipt",)
    RECEIPT_FIELD_NUMBER: _ClassVar[int]
    receipt: Receipt
    def __init__(self, receipt: _Optional[_Union[Receipt, _Mapping]] = ...) -> None: ...

class ObserveRequest(_message.Message):
    __slots__ = ("session", "after_cursor")
    SESSION_FIELD_NUMBER: _ClassVar[int]
    AFTER_CURSOR_FIELD_NUMBER: _ClassVar[int]
    session: SessionRef
    after_cursor: str
    def __init__(self, session: _Optional[_Union[SessionRef, _Mapping]] = ..., after_cursor: _Optional[str] = ...) -> None: ...

class TextUpdate(_message.Message):
    __slots__ = ("speaker", "text")
    SPEAKER_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    speaker: Speaker
    text: str
    def __init__(self, speaker: _Optional[_Union[Speaker, str]] = ..., text: _Optional[str] = ...) -> None: ...

class ToolActivity(_message.Message):
    __slots__ = ("call_id", "name", "phase", "summary")
    CALL_ID_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    PHASE_FIELD_NUMBER: _ClassVar[int]
    SUMMARY_FIELD_NUMBER: _ClassVar[int]
    call_id: str
    name: str
    phase: str
    summary: str
    def __init__(self, call_id: _Optional[str] = ..., name: _Optional[str] = ..., phase: _Optional[str] = ..., summary: _Optional[str] = ...) -> None: ...

class Choice(_message.Message):
    __slots__ = ("id", "label")
    ID_FIELD_NUMBER: _ClassVar[int]
    LABEL_FIELD_NUMBER: _ClassVar[int]
    id: str
    label: str
    def __init__(self, id: _Optional[str] = ..., label: _Optional[str] = ...) -> None: ...

class InputRequest(_message.Message):
    __slots__ = ("request_id", "prompt", "choices")
    REQUEST_ID_FIELD_NUMBER: _ClassVar[int]
    PROMPT_FIELD_NUMBER: _ClassVar[int]
    CHOICES_FIELD_NUMBER: _ClassVar[int]
    request_id: str
    prompt: str
    choices: _containers.RepeatedCompositeFieldContainer[Choice]
    def __init__(self, request_id: _Optional[str] = ..., prompt: _Optional[str] = ..., choices: _Optional[_Iterable[_Union[Choice, _Mapping]]] = ...) -> None: ...

class TurnEnded(_message.Message):
    __slots__ = ("reason",)
    REASON_FIELD_NUMBER: _ClassVar[int]
    reason: str
    def __init__(self, reason: _Optional[str] = ...) -> None: ...

class HistoryGap(_message.Message):
    __slots__ = ("reason",)
    REASON_FIELD_NUMBER: _ClassVar[int]
    reason: str
    def __init__(self, reason: _Optional[str] = ...) -> None: ...

class Extension(_message.Message):
    __slots__ = ("type", "media_type", "data")
    TYPE_FIELD_NUMBER: _ClassVar[int]
    MEDIA_TYPE_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    type: str
    media_type: str
    data: bytes
    def __init__(self, type: _Optional[str] = ..., media_type: _Optional[str] = ..., data: _Optional[bytes] = ...) -> None: ...

class Observation(_message.Message):
    __slots__ = ("session", "cursor", "turn_id", "text", "tool", "input", "turn_ended", "gap", "extension", "source_record", "source_cursor")
    SESSION_FIELD_NUMBER: _ClassVar[int]
    CURSOR_FIELD_NUMBER: _ClassVar[int]
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    TOOL_FIELD_NUMBER: _ClassVar[int]
    INPUT_FIELD_NUMBER: _ClassVar[int]
    TURN_ENDED_FIELD_NUMBER: _ClassVar[int]
    GAP_FIELD_NUMBER: _ClassVar[int]
    EXTENSION_FIELD_NUMBER: _ClassVar[int]
    SOURCE_RECORD_FIELD_NUMBER: _ClassVar[int]
    SOURCE_CURSOR_FIELD_NUMBER: _ClassVar[int]
    session: SessionRef
    cursor: str
    turn_id: str
    text: TextUpdate
    tool: ToolActivity
    input: InputRequest
    turn_ended: TurnEnded
    gap: HistoryGap
    extension: Extension
    source_record: Extension
    source_cursor: str
    def __init__(self, session: _Optional[_Union[SessionRef, _Mapping]] = ..., cursor: _Optional[str] = ..., turn_id: _Optional[str] = ..., text: _Optional[_Union[TextUpdate, _Mapping]] = ..., tool: _Optional[_Union[ToolActivity, _Mapping]] = ..., input: _Optional[_Union[InputRequest, _Mapping]] = ..., turn_ended: _Optional[_Union[TurnEnded, _Mapping]] = ..., gap: _Optional[_Union[HistoryGap, _Mapping]] = ..., extension: _Optional[_Union[Extension, _Mapping]] = ..., source_record: _Optional[_Union[Extension, _Mapping]] = ..., source_cursor: _Optional[str] = ...) -> None: ...

class ListSessionsRequest(_message.Message):
    __slots__ = ("harness_instance", "working_directory", "cursor", "limit")
    HARNESS_INSTANCE_FIELD_NUMBER: _ClassVar[int]
    WORKING_DIRECTORY_FIELD_NUMBER: _ClassVar[int]
    CURSOR_FIELD_NUMBER: _ClassVar[int]
    LIMIT_FIELD_NUMBER: _ClassVar[int]
    harness_instance: str
    working_directory: str
    cursor: str
    limit: int
    def __init__(self, harness_instance: _Optional[str] = ..., working_directory: _Optional[str] = ..., cursor: _Optional[str] = ..., limit: _Optional[int] = ...) -> None: ...

class ListSessionsResponse(_message.Message):
    __slots__ = ("sessions", "next_cursor")
    SESSIONS_FIELD_NUMBER: _ClassVar[int]
    NEXT_CURSOR_FIELD_NUMBER: _ClassVar[int]
    sessions: _containers.RepeatedCompositeFieldContainer[Session]
    next_cursor: str
    def __init__(self, sessions: _Optional[_Iterable[_Union[Session, _Mapping]]] = ..., next_cursor: _Optional[str] = ...) -> None: ...

class ReadHistoryRequest(_message.Message):
    __slots__ = ("session", "cursor", "limit")
    SESSION_FIELD_NUMBER: _ClassVar[int]
    CURSOR_FIELD_NUMBER: _ClassVar[int]
    LIMIT_FIELD_NUMBER: _ClassVar[int]
    session: SessionRef
    cursor: str
    limit: int
    def __init__(self, session: _Optional[_Union[SessionRef, _Mapping]] = ..., cursor: _Optional[str] = ..., limit: _Optional[int] = ...) -> None: ...

class ReadHistoryResponse(_message.Message):
    __slots__ = ("observations", "next_cursor")
    OBSERVATIONS_FIELD_NUMBER: _ClassVar[int]
    NEXT_CURSOR_FIELD_NUMBER: _ClassVar[int]
    observations: _containers.RepeatedCompositeFieldContainer[Observation]
    next_cursor: str
    def __init__(self, observations: _Optional[_Iterable[_Union[Observation, _Mapping]]] = ..., next_cursor: _Optional[str] = ...) -> None: ...
