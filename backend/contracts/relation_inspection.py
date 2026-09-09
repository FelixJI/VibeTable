"""Read-only, paged integrity reports for persisted relation pairs."""

from typing import Annotated, Literal

from pydantic import ConfigDict, Field

from backend.contracts.table import CamelModel

RelationInspectionCode = Literal[
    "metadata_asymmetric",
    "metadata_invalid",
    "dangling",
    "duplicate",
    "one_conflict",
    "missing_reciprocal",
    "presence_mismatch",
    "invalid_value",
    "scan_limit",
]
NonNegativeCount = Annotated[int, Field(ge=0, strict=True)]


class InspectionWireModel(CamelModel):
    model_config = ConfigDict(populate_by_name=False, validate_by_name=False)


class RelationInspectionEndpoint(InspectionWireModel):
    table_id: str = Field(max_length=128, strict=True)
    field_id: str = Field(max_length=128, strict=True)
    schema_revision: str = Field(max_length=256, strict=True)
    data_revision: NonNegativeCount


class RelationInspectionCursor(InspectionWireModel):
    pair_id: str = Field(min_length=1, max_length=128, strict=True)
    endpoints: tuple[RelationInspectionEndpoint, RelationInspectionEndpoint]
    after: tuple[
        Annotated[str, Field(max_length=200, strict=True)],
        Annotated[str, Field(max_length=200, strict=True)],
    ]
    done: tuple[Annotated[bool, Field(strict=True)], Annotated[bool, Field(strict=True)]]
    incomplete: bool = Field(strict=True)


class RelationInspectPairRequest(InspectionWireModel):
    table_id: str = Field(min_length=1, max_length=128, strict=True)
    field_id: str = Field(min_length=1, max_length=128, strict=True)
    limit: int = Field(default=100, ge=1, le=200, strict=True)
    cursor: RelationInspectionCursor | None = None


class RelationInspectionFinding(InspectionWireModel):
    code: RelationInspectionCode
    endpoint: Literal[0, 1]
    record_id: str = Field(default="", exclude_if=lambda value: not value)
    target_id: str = Field(default="", exclude_if=lambda value: not value)
    detail: str = Field(default="", exclude_if=lambda value: not value)


class RelationInspectionReport(InspectionWireModel):
    """Counts are page-local; complete measures coverage, never integrity."""

    pair_id: str
    endpoints: tuple[RelationInspectionEndpoint, RelationInspectionEndpoint]
    counts: dict[RelationInspectionCode, NonNegativeCount]
    samples: list[RelationInspectionFinding] = Field(max_length=50)
    samples_truncated: bool
    rows_scanned: tuple[NonNegativeCount, NonNegativeCount]
    page_complete: bool
    finished: bool
    complete: bool
    next: RelationInspectionCursor | None = Field(
        default=None, exclude_if=lambda value: value is None
    )
