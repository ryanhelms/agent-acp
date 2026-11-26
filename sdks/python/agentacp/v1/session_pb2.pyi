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

class WireMessageKind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    WIRE_MESSAGE_KIND_UNSPECIFIED: _ClassVar[WireMessageKind]
    WIRE_MESSAGE_KIND_REQUEST: _ClassVar[WireMessageKind]
    WIRE_MESSAGE_KIND_NOTIFICATION: _ClassVar[WireMessageKind]
    WIRE_MESSAGE_KIND_RESULT: _ClassVar[WireMessageKind]
    WIRE_MESSAGE_KIND_ERROR: _ClassVar[WireMessageKind]
DELIVERY_STATUS_UNSPECIFIED: DeliveryStatus
DELIVERY_STATUS_ACCEPTED: DeliveryStatus
DELIVERY_STATUS_DELIVERED: DeliveryStatus
DELIVERY_STATUS_UNKNOWN: DeliveryStatus
DELIVERY_STATUS_UNSUPPORTED: DeliveryStatus
SPEAKER_UNSPECIFIED: Speaker
SPEAKER_USER: Speaker
SPEAKER_ASSISTANT: Speaker
SPEAKER_SYSTEM: Speaker
WIRE_MESSAGE_KIND_UNSPECIFIED: WireMessageKind
WIRE_MESSAGE_KIND_REQUEST: WireMessageKind
WIRE_MESSAGE_KIND_NOTIFICATION: WireMessageKind
WIRE_MESSAGE_KIND_RESULT: WireMessageKind
WIRE_MESSAGE_KIND_ERROR: WireMessageKind

class SessionRef(_message.Message):
    __slots__ = ("harness_instance", "session_id")
    HARNESS_INSTANCE_FIELD_NUMBER: _ClassVar[int]
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    harness_instance: str
    session_id: str
    def __init__(self, harness_instance: _Optional[str] = ..., session_id: _Optional[str] = ...) -> None: ...

class Capabilities(_message.Message):
    __slots__ = ("can_resume", "can_answer", "can_interrupt", "can_list_sessions", "can_read_history", "can_prompt_image", "can_prompt_audio", "can_prompt_embedded_context")
    CAN_RESUME_FIELD_NUMBER: _ClassVar[int]
    CAN_ANSWER_FIELD_NUMBER: _ClassVar[int]
    CAN_INTERRUPT_FIELD_NUMBER: _ClassVar[int]
    CAN_LIST_SESSIONS_FIELD_NUMBER: _ClassVar[int]
    CAN_READ_HISTORY_FIELD_NUMBER: _ClassVar[int]
    CAN_PROMPT_IMAGE_FIELD_NUMBER: _ClassVar[int]
    CAN_PROMPT_AUDIO_FIELD_NUMBER: _ClassVar[int]
    CAN_PROMPT_EMBEDDED_CONTEXT_FIELD_NUMBER: _ClassVar[int]
    can_resume: bool
    can_answer: bool
    can_interrupt: bool
    can_list_sessions: bool
    can_read_history: bool
    can_prompt_image: bool
    can_prompt_audio: bool
    can_prompt_embedded_context: bool
    def __init__(self, can_resume: _Optional[bool] = ..., can_answer: _Optional[bool] = ..., can_interrupt: _Optional[bool] = ..., can_list_sessions: _Optional[bool] = ..., can_read_history: _Optional[bool] = ..., can_prompt_image: _Optional[bool] = ..., can_prompt_audio: _Optional[bool] = ..., can_prompt_embedded_context: _Optional[bool] = ...) -> None: ...

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
    __slots__ = ("text", "image", "audio", "resource_link", "resource")
    TEXT_FIELD_NUMBER: _ClassVar[int]
    IMAGE_FIELD_NUMBER: _ClassVar[int]
    AUDIO_FIELD_NUMBER: _ClassVar[int]
    RESOURCE_LINK_FIELD_NUMBER: _ClassVar[int]
    RESOURCE_FIELD_NUMBER: _ClassVar[int]
    text: str
    image: PromptMedia
    audio: PromptMedia
    resource_link: PromptResourceLink
    resource: PromptResource
    def __init__(self, text: _Optional[str] = ..., image: _Optional[_Union[PromptMedia, _Mapping]] = ..., audio: _Optional[_Union[PromptMedia, _Mapping]] = ..., resource_link: _Optional[_Union[PromptResourceLink, _Mapping]] = ..., resource: _Optional[_Union[PromptResource, _Mapping]] = ...) -> None: ...

class PromptMedia(_message.Message):
    __slots__ = ("mime_type", "data")
    MIME_TYPE_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    mime_type: str
    data: bytes
    def __init__(self, mime_type: _Optional[str] = ..., data: _Optional[bytes] = ...) -> None: ...

class PromptResourceLink(_message.Message):
    __slots__ = ("uri", "name", "mime_type")
    URI_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    MIME_TYPE_FIELD_NUMBER: _ClassVar[int]
    uri: str
    name: str
    mime_type: str
    def __init__(self, uri: _Optional[str] = ..., name: _Optional[str] = ..., mime_type: _Optional[str] = ...) -> None: ...

class PromptResource(_message.Message):
    __slots__ = ("uri", "mime_type", "text", "blob")
    URI_FIELD_NUMBER: _ClassVar[int]
    MIME_TYPE_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    BLOB_FIELD_NUMBER: _ClassVar[int]
    uri: str
    mime_type: str
    text: str
    blob: bytes
    def __init__(self, uri: _Optional[str] = ..., mime_type: _Optional[str] = ..., text: _Optional[str] = ..., blob: _Optional[bytes] = ...) -> None: ...

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

class MessageContent(_message.Message):
    __slots__ = ("speaker", "thought", "kind", "text", "uri", "mime_type", "name", "title", "description", "size")
    SPEAKER_FIELD_NUMBER: _ClassVar[int]
    THOUGHT_FIELD_NUMBER: _ClassVar[int]
    KIND_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    URI_FIELD_NUMBER: _ClassVar[int]
    MIME_TYPE_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    SIZE_FIELD_NUMBER: _ClassVar[int]
    speaker: Speaker
    thought: bool
    kind: str
    text: str
    uri: str
    mime_type: str
    name: str
    title: str
    description: str
    size: int
    def __init__(self, speaker: _Optional[_Union[Speaker, str]] = ..., thought: _Optional[bool] = ..., kind: _Optional[str] = ..., text: _Optional[str] = ..., uri: _Optional[str] = ..., mime_type: _Optional[str] = ..., name: _Optional[str] = ..., title: _Optional[str] = ..., description: _Optional[str] = ..., size: _Optional[int] = ...) -> None: ...

class PlanSnapshot(_message.Message):
    __slots__ = ("entries",)
    class Entry(_message.Message):
        __slots__ = ("content", "priority", "status")
        CONTENT_FIELD_NUMBER: _ClassVar[int]
        PRIORITY_FIELD_NUMBER: _ClassVar[int]
        STATUS_FIELD_NUMBER: _ClassVar[int]
        content: str
        priority: str
        status: str
        def __init__(self, content: _Optional[str] = ..., priority: _Optional[str] = ..., status: _Optional[str] = ...) -> None: ...
    ENTRIES_FIELD_NUMBER: _ClassVar[int]
    entries: _containers.RepeatedCompositeFieldContainer[PlanSnapshot.Entry]
    def __init__(self, entries: _Optional[_Iterable[_Union[PlanSnapshot.Entry, _Mapping]]] = ...) -> None: ...

class AvailableCommands(_message.Message):
    __slots__ = ("commands",)
    class Command(_message.Message):
        __slots__ = ("name", "description", "input_hint")
        NAME_FIELD_NUMBER: _ClassVar[int]
        DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
        INPUT_HINT_FIELD_NUMBER: _ClassVar[int]
        name: str
        description: str
        input_hint: str
        def __init__(self, name: _Optional[str] = ..., description: _Optional[str] = ..., input_hint: _Optional[str] = ...) -> None: ...
    COMMANDS_FIELD_NUMBER: _ClassVar[int]
    commands: _containers.RepeatedCompositeFieldContainer[AvailableCommands.Command]
    def __init__(self, commands: _Optional[_Iterable[_Union[AvailableCommands.Command, _Mapping]]] = ...) -> None: ...

class ModeChanged(_message.Message):
    __slots__ = ("mode_id",)
    MODE_ID_FIELD_NUMBER: _ClassVar[int]
    mode_id: str
    def __init__(self, mode_id: _Optional[str] = ...) -> None: ...

class ConfigurationSnapshot(_message.Message):
    __slots__ = ("options",)
    class Choice(_message.Message):
        __slots__ = ("value", "name", "description")
        VALUE_FIELD_NUMBER: _ClassVar[int]
        NAME_FIELD_NUMBER: _ClassVar[int]
        DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
        value: str
        name: str
        description: str
        def __init__(self, value: _Optional[str] = ..., name: _Optional[str] = ..., description: _Optional[str] = ...) -> None: ...
    class Group(_message.Message):
        __slots__ = ("id", "name", "choices")
        ID_FIELD_NUMBER: _ClassVar[int]
        NAME_FIELD_NUMBER: _ClassVar[int]
        CHOICES_FIELD_NUMBER: _ClassVar[int]
        id: str
        name: str
        choices: _containers.RepeatedCompositeFieldContainer[ConfigurationSnapshot.Choice]
        def __init__(self, id: _Optional[str] = ..., name: _Optional[str] = ..., choices: _Optional[_Iterable[_Union[ConfigurationSnapshot.Choice, _Mapping]]] = ...) -> None: ...
    class Option(_message.Message):
        __slots__ = ("id", "name", "description", "category", "kind", "selected", "enabled", "choices", "groups")
        ID_FIELD_NUMBER: _ClassVar[int]
        NAME_FIELD_NUMBER: _ClassVar[int]
        DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
        CATEGORY_FIELD_NUMBER: _ClassVar[int]
        KIND_FIELD_NUMBER: _ClassVar[int]
        SELECTED_FIELD_NUMBER: _ClassVar[int]
        ENABLED_FIELD_NUMBER: _ClassVar[int]
        CHOICES_FIELD_NUMBER: _ClassVar[int]
        GROUPS_FIELD_NUMBER: _ClassVar[int]
        id: str
        name: str
        description: str
        category: str
        kind: str
        selected: str
        enabled: bool
        choices: _containers.RepeatedCompositeFieldContainer[ConfigurationSnapshot.Choice]
        groups: _containers.RepeatedCompositeFieldContainer[ConfigurationSnapshot.Group]
        def __init__(self, id: _Optional[str] = ..., name: _Optional[str] = ..., description: _Optional[str] = ..., category: _Optional[str] = ..., kind: _Optional[str] = ..., selected: _Optional[str] = ..., enabled: _Optional[bool] = ..., choices: _Optional[_Iterable[_Union[ConfigurationSnapshot.Choice, _Mapping]]] = ..., groups: _Optional[_Iterable[_Union[ConfigurationSnapshot.Group, _Mapping]]] = ...) -> None: ...
    OPTIONS_FIELD_NUMBER: _ClassVar[int]
    options: _containers.RepeatedCompositeFieldContainer[ConfigurationSnapshot.Option]
    def __init__(self, options: _Optional[_Iterable[_Union[ConfigurationSnapshot.Option, _Mapping]]] = ...) -> None: ...

class SessionInfoChanged(_message.Message):
    __slots__ = ("title", "updated_at", "clear_title", "clear_updated_at")
    TITLE_FIELD_NUMBER: _ClassVar[int]
    UPDATED_AT_FIELD_NUMBER: _ClassVar[int]
    CLEAR_TITLE_FIELD_NUMBER: _ClassVar[int]
    CLEAR_UPDATED_AT_FIELD_NUMBER: _ClassVar[int]
    title: str
    updated_at: str
    clear_title: bool
    clear_updated_at: bool
    def __init__(self, title: _Optional[str] = ..., updated_at: _Optional[str] = ..., clear_title: _Optional[bool] = ..., clear_updated_at: _Optional[bool] = ...) -> None: ...

class UsageChanged(_message.Message):
    __slots__ = ("used", "size", "cost")
    class Cost(_message.Message):
        __slots__ = ("amount", "currency")
        AMOUNT_FIELD_NUMBER: _ClassVar[int]
        CURRENCY_FIELD_NUMBER: _ClassVar[int]
        amount: float
        currency: str
        def __init__(self, amount: _Optional[float] = ..., currency: _Optional[str] = ...) -> None: ...
    USED_FIELD_NUMBER: _ClassVar[int]
    SIZE_FIELD_NUMBER: _ClassVar[int]
    COST_FIELD_NUMBER: _ClassVar[int]
    used: int
    size: int
    cost: UsageChanged.Cost
    def __init__(self, used: _Optional[int] = ..., size: _Optional[int] = ..., cost: _Optional[_Union[UsageChanged.Cost, _Mapping]] = ...) -> None: ...

class SessionUpdate(_message.Message):
    __slots__ = ("plan", "commands", "mode", "configuration", "info", "usage")
    PLAN_FIELD_NUMBER: _ClassVar[int]
    COMMANDS_FIELD_NUMBER: _ClassVar[int]
    MODE_FIELD_NUMBER: _ClassVar[int]
    CONFIGURATION_FIELD_NUMBER: _ClassVar[int]
    INFO_FIELD_NUMBER: _ClassVar[int]
    USAGE_FIELD_NUMBER: _ClassVar[int]
    plan: PlanSnapshot
    commands: AvailableCommands
    mode: ModeChanged
    configuration: ConfigurationSnapshot
    info: SessionInfoChanged
    usage: UsageChanged
    def __init__(self, plan: _Optional[_Union[PlanSnapshot, _Mapping]] = ..., commands: _Optional[_Union[AvailableCommands, _Mapping]] = ..., mode: _Optional[_Union[ModeChanged, _Mapping]] = ..., configuration: _Optional[_Union[ConfigurationSnapshot, _Mapping]] = ..., info: _Optional[_Union[SessionInfoChanged, _Mapping]] = ..., usage: _Optional[_Union[UsageChanged, _Mapping]] = ...) -> None: ...

class WireProvenance(_message.Message):
    __slots__ = ("kind", "protocol_version", "method", "id_json", "additional_fields_json", "omitted_additional_fields_bytes", "omitted_id_bytes", "id_json_bytes", "additional_fields_json_bytes", "protocol_version_json_bytes", "omitted_protocol_version_bytes")
    KIND_FIELD_NUMBER: _ClassVar[int]
    PROTOCOL_VERSION_FIELD_NUMBER: _ClassVar[int]
    METHOD_FIELD_NUMBER: _ClassVar[int]
    ID_JSON_FIELD_NUMBER: _ClassVar[int]
    ADDITIONAL_FIELDS_JSON_FIELD_NUMBER: _ClassVar[int]
    OMITTED_ADDITIONAL_FIELDS_BYTES_FIELD_NUMBER: _ClassVar[int]
    OMITTED_ID_BYTES_FIELD_NUMBER: _ClassVar[int]
    ID_JSON_BYTES_FIELD_NUMBER: _ClassVar[int]
    ADDITIONAL_FIELDS_JSON_BYTES_FIELD_NUMBER: _ClassVar[int]
    PROTOCOL_VERSION_JSON_BYTES_FIELD_NUMBER: _ClassVar[int]
    OMITTED_PROTOCOL_VERSION_BYTES_FIELD_NUMBER: _ClassVar[int]
    kind: WireMessageKind
    protocol_version: str
    method: str
    id_json: str
    additional_fields_json: str
    omitted_additional_fields_bytes: int
    omitted_id_bytes: int
    id_json_bytes: bytes
    additional_fields_json_bytes: bytes
    protocol_version_json_bytes: bytes
    omitted_protocol_version_bytes: int
    def __init__(self, kind: _Optional[_Union[WireMessageKind, str]] = ..., protocol_version: _Optional[str] = ..., method: _Optional[str] = ..., id_json: _Optional[str] = ..., additional_fields_json: _Optional[str] = ..., omitted_additional_fields_bytes: _Optional[int] = ..., omitted_id_bytes: _Optional[int] = ..., id_json_bytes: _Optional[bytes] = ..., additional_fields_json_bytes: _Optional[bytes] = ..., protocol_version_json_bytes: _Optional[bytes] = ..., omitted_protocol_version_bytes: _Optional[int] = ...) -> None: ...

class Observation(_message.Message):
    __slots__ = ("session", "cursor", "turn_id", "text", "tool", "input", "turn_ended", "gap", "extension", "session_update", "content", "source_record", "source_cursor", "wire_provenance")
    SESSION_FIELD_NUMBER: _ClassVar[int]
    CURSOR_FIELD_NUMBER: _ClassVar[int]
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    TOOL_FIELD_NUMBER: _ClassVar[int]
    INPUT_FIELD_NUMBER: _ClassVar[int]
    TURN_ENDED_FIELD_NUMBER: _ClassVar[int]
    GAP_FIELD_NUMBER: _ClassVar[int]
    EXTENSION_FIELD_NUMBER: _ClassVar[int]
    SESSION_UPDATE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    SOURCE_RECORD_FIELD_NUMBER: _ClassVar[int]
    SOURCE_CURSOR_FIELD_NUMBER: _ClassVar[int]
    WIRE_PROVENANCE_FIELD_NUMBER: _ClassVar[int]
    session: SessionRef
    cursor: str
    turn_id: str
    text: TextUpdate
    tool: ToolActivity
    input: InputRequest
    turn_ended: TurnEnded
    gap: HistoryGap
    extension: Extension
    session_update: SessionUpdate
    content: MessageContent
    source_record: Extension
    source_cursor: str
    wire_provenance: WireProvenance
    def __init__(self, session: _Optional[_Union[SessionRef, _Mapping]] = ..., cursor: _Optional[str] = ..., turn_id: _Optional[str] = ..., text: _Optional[_Union[TextUpdate, _Mapping]] = ..., tool: _Optional[_Union[ToolActivity, _Mapping]] = ..., input: _Optional[_Union[InputRequest, _Mapping]] = ..., turn_ended: _Optional[_Union[TurnEnded, _Mapping]] = ..., gap: _Optional[_Union[HistoryGap, _Mapping]] = ..., extension: _Optional[_Union[Extension, _Mapping]] = ..., session_update: _Optional[_Union[SessionUpdate, _Mapping]] = ..., content: _Optional[_Union[MessageContent, _Mapping]] = ..., source_record: _Optional[_Union[Extension, _Mapping]] = ..., source_cursor: _Optional[str] = ..., wire_provenance: _Optional[_Union[WireProvenance, _Mapping]] = ...) -> None: ...

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
