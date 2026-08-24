#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "acryl-datahub==1.6.0",
# ]
# ///
"""Discover datasets and idempotently enrich them in DataHub.

The module keeps discovery, validation, and planning independent of the DataHub SDK so
that they can be tested offline. DataHub imports occur only in DataHubClient methods;
the jobs image supplies the exact SDK pinned in config/versions.lock.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import re
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable, Mapping, Protocol, Sequence

SCHEMA_VERSION = 1
MANIFEST_SCHEMA_VERSION = 1
DEFAULT_DATABASE = "saas_accounts_demo"
DEFAULT_PLATFORM = "glue"
DEFAULT_ENV = "DEV"
DEFAULT_DISCOVERY_TIMEOUT_SECONDS = 60.0
DEFAULT_DISCOVERY_INTERVAL_SECONDS = 3.0
FIXED_PROPERTIES: tuple[dict[str, Any], ...] = (
    {"id": "semantic.models", "display_name": "Semantic Models", "cardinality": "MULTIPLE"},
    {"id": "semantic.dataset", "display_name": "Semantic Dataset", "cardinality": "SINGLE"},
    {"id": "semantic.physical_table", "display_name": "Semantic Physical Table", "cardinality": "SINGLE"},
    {"id": "semantic.execution", "display_name": "Semantic Execution", "cardinality": "SINGLE"},
    {"id": "semantic.mcp_tool", "display_name": "Semantic MCP Tool", "cardinality": "SINGLE"},
)
DEFAULT_EXPECTED_ASSETS = (
    "account",
    "account_contact",
    "account_feature_entitlement",
    "account_feature_monthly",
    "contract",
    "date_dim",
    "plan",
    "product_feature",
    "subscription_monthly",
    "support_ticket",
    "usage_daily",
)
_SECRET_PATTERNS = (
    re.compile(r"(?i)(authorization\s*[:=]\s*bearer\s+)[^\s,;]+"),
    re.compile(r"(?i)(token\s*[:=]\s*)[^\s,;]+"),
)


class EnrichmentError(RuntimeError):
    """Expected, actionable enrichment failure."""


class MetadataValidationError(EnrichmentError):
    """The declarative metadata document violates schema.md."""


class DiscoveryError(EnrichmentError):
    """Canonical ingestion-created assets cannot be resolved exactly."""


class MissingAssetsError(DiscoveryError):
    """Ingested assets are not visible in DataHub search yet."""


class ApplyError(EnrichmentError):
    """DataHub rejected an enrichment operation."""


class RedactingFilter(logging.Filter):
    """Redact configured secrets and token-shaped text before any log is emitted."""

    def __init__(self, secrets: Iterable[str] = ()) -> None:
        super().__init__()
        self.secrets = tuple(value for value in secrets if value)

    def filter(self, record: logging.LogRecord) -> bool:
        message = record.getMessage()
        for secret in self.secrets:
            message = message.replace(secret, "[REDACTED]")
        for pattern in _SECRET_PATTERNS:
            message = pattern.sub(r"\1[REDACTED]", message)
        record.msg = message
        record.args = ()
        return True


def configure_logging(token: str | None = None) -> logging.Logger:
    handler = logging.StreamHandler(sys.stderr)
    handler.addFilter(RedactingFilter([token or ""]))
    handler.setFormatter(logging.Formatter("%(levelname)s %(message)s"))
    logger = logging.getLogger("datahub-enrichment")
    logger.handlers = [handler]
    logger.setLevel(logging.INFO)
    logger.propagate = False
    return logger


class DiscoveryClient(Protocol):
    def list_dataset_urns(
        self, *, platform: str, platform_instance: str, env: str, query: str
    ) -> Iterable[str]: ...


@dataclass(frozen=True)
class Operation:
    """A deterministic desired-state operation; duplicate keys are forbidden."""

    kind: str
    key: str
    target: str
    payload: Mapping[str, Any]

    def as_dict(self) -> dict[str, Any]:
        return {
            "kind": self.kind,
            "key": self.key,
            "target": self.target,
            "payload": dict(self.payload),
        }


_REQUIRED_TOP_LEVEL = {
    "version",
    "domains",
    "groups",
    "glossary_nodes",
    "glossary_terms",
    "tags",
    "upstream_datasets",
    "transformation_jobs",
    "assets",
}
_ALLOWED_TOP_LEVEL = _REQUIRED_TOP_LEVEL | {"expected_assets"}
_ENTITY_FIELDS = {
    "domains": ({"id", "name"}, {"id", "name", "description"}),
    "groups": ({"id", "name"}, {"id", "name", "description", "email"}),
    "glossary_nodes": ({"id", "name"}, {"id", "name", "description", "parent"}),
    "glossary_terms": ({"id", "name", "definition"}, {"id", "name", "definition", "node"}),
    "tags": ({"id", "name"}, {"id", "name", "description", "color"}),
    "upstream_datasets": (
        {"id", "name"},
        {"id", "name", "description", "platform", "platform_instance", "env"},
    ),
    "transformation_jobs": (
        {"id", "flow_id", "name", "inputs", "outputs"},
        {"id", "flow_id", "name", "description", "inputs", "outputs", "owners"},
    ),
}
_ASSET_ALLOWED = {
    "domain",
    "owners",
    "glossary_terms",
    "tags",
    "fields",
    "documentation",
    "certification",
    "deprecation",
    "structured_properties",
    "upstreams",
}
_FIELD_ALLOWED = {"description", "tags", "glossary_terms"}
_SEMANTIC_PROPERTY_IDS = {item["id"] for item in FIXED_PROPERTIES}


def _fail(path: str, message: str) -> None:
    raise MetadataValidationError(f"{path}: {message}")


def _mapping(value: Any, path: str) -> Mapping[str, Any]:
    if not isinstance(value, dict):
        _fail(path, "must be a mapping")
    return value


def _list(value: Any, path: str) -> list[Any]:
    if not isinstance(value, list):
        _fail(path, "must be a list")
    return value


def _nonempty_string(value: Any, path: str) -> str:
    if not isinstance(value, str) or not value.strip():
        _fail(path, "must be a non-empty string")
    return value


def _string_list(value: Any, path: str) -> list[str]:
    values = _list(value, path)
    for index, item in enumerate(values):
        _nonempty_string(item, f"{path}[{index}]")
    if len(values) != len(set(values)):
        _fail(path, "must not contain duplicates")
    return values


def _check_fields(
    item: Mapping[str, Any], path: str, required: set[str], allowed: set[str]
) -> None:
    missing = sorted(required - set(item))
    unknown = sorted(set(item) - allowed)
    if missing:
        _fail(path, f"missing required keys: {', '.join(missing)}")
    if unknown:
        _fail(path, f"unknown keys: {', '.join(unknown)}")


def validate_metadata(raw: Any) -> dict[str, Any]:
    """Validate and return a JSON-compatible copy of the declarative document."""
    doc = _mapping(raw, "metadata")
    _check_fields(doc, "metadata", _REQUIRED_TOP_LEVEL, _ALLOWED_TOP_LEVEL)
    if doc["version"] != SCHEMA_VERSION:
        _fail("metadata.version", f"must equal {SCHEMA_VERSION}")

    result = json.loads(json.dumps(doc))
    identifiers: dict[str, set[str]] = {}
    for section, (required, allowed) in _ENTITY_FIELDS.items():
        identifiers[section] = set()
        for index, raw_item in enumerate(_list(doc[section], f"metadata.{section}")):
            path = f"metadata.{section}[{index}]"
            item = _mapping(raw_item, path)
            _check_fields(item, path, required, allowed)
            for field in required:
                if field not in {"inputs", "outputs"}:
                    _nonempty_string(item[field], f"{path}.{field}")
            if item["id"] in identifiers[section]:
                _fail(f"{path}.id", f"duplicate id {item['id']!r}")
            identifiers[section].add(item["id"])
            for field in ("inputs", "outputs", "owners"):
                if field in item:
                    _string_list(item[field], f"{path}.{field}")

    expected = doc.get("expected_assets", list(DEFAULT_EXPECTED_ASSETS))
    expected_assets = _string_list(expected, "metadata.expected_assets")
    if not expected_assets:
        _fail("metadata.expected_assets", "must not be empty")
    result["expected_assets"] = expected_assets

    assets = _mapping(doc["assets"], "metadata.assets")
    unknown_assets = sorted(set(assets) - set(expected_assets))
    if unknown_assets:
        _fail("metadata.assets", f"keys are not expected assets: {', '.join(unknown_assets)}")
    for table, raw_asset in assets.items():
        path = f"metadata.assets.{table}"
        asset = _mapping(raw_asset, path)
        unknown = sorted(set(asset) - _ASSET_ALLOWED)
        if unknown:
            _fail(path, f"unknown keys: {', '.join(unknown)}")
        for field in ("owners", "glossary_terms", "tags", "upstreams"):
            if field in asset:
                _string_list(asset[field], f"{path}.{field}")
        if "domain" in asset:
            _nonempty_string(asset["domain"], f"{path}.domain")
        if "documentation" in asset:
            _nonempty_string(asset["documentation"], f"{path}.documentation")
        if "certification" in asset and asset["certification"] not in {"CERTIFIED", "UNVERIFIED"}:
            _fail(f"{path}.certification", "must be CERTIFIED or UNVERIFIED")
        if "deprecation" in asset:
            dep = _mapping(asset["deprecation"], f"{path}.deprecation")
            _check_fields(dep, f"{path}.deprecation", {"deprecated", "note"}, {"deprecated", "note", "replacement"})
            if not isinstance(dep["deprecated"], bool):
                _fail(f"{path}.deprecation.deprecated", "must be a boolean")
            _nonempty_string(dep["note"], f"{path}.deprecation.note")
        if "structured_properties" in asset:
            properties = _mapping(asset["structured_properties"], f"{path}.structured_properties")
            unknown_properties = sorted(set(properties) - _SEMANTIC_PROPERTY_IDS)
            if unknown_properties:
                _fail(f"{path}.structured_properties", f"unknown fixed property IDs: {', '.join(unknown_properties)}")
            for prop, value in properties.items():
                values = value if isinstance(value, list) else [value]
                if not values or any(not isinstance(v, (str, int, float)) or isinstance(v, bool) for v in values):
                    _fail(f"{path}.structured_properties.{prop}", "must be a scalar or non-empty scalar list")
                if prop != "semantic.models" and len(values) != 1:
                    _fail(f"{path}.structured_properties.{prop}", "is single-valued")
        if "fields" in asset:
            fields = _mapping(asset["fields"], f"{path}.fields")
            for field_name, raw_field in fields.items():
                _nonempty_string(field_name, f"{path}.fields key")
                field = _mapping(raw_field, f"{path}.fields.{field_name}")
                _check_fields(
                    field,
                    f"{path}.fields.{field_name}",
                    {"description"},
                    _FIELD_ALLOWED,
                )
                _nonempty_string(field["description"], f"{path}.fields.{field_name}.description")
                for key in ("tags", "glossary_terms"):
                    if key in field:
                        _string_list(field[key], f"{path}.fields.{field_name}.{key}")

    references = {
        "domain": identifiers["domains"],
        "owners": identifiers["groups"],
        "glossary_terms": identifiers["glossary_terms"],
        "tags": identifiers["tags"],
        "upstreams": identifiers["upstream_datasets"],
    }
    for table, asset in assets.items():
        for field, valid in references.items():
            values = [asset[field]] if field == "domain" and field in asset else asset.get(field, [])
            for value in values:
                if value not in valid:
                    _fail(f"metadata.assets.{table}.{field}", f"unknown reference {value!r}")
        for field_name, field in asset.get("fields", {}).items():
            for key in ("tags", "glossary_terms"):
                for value in field.get(key, []):
                    if value not in references[key]:
                        _fail(f"metadata.assets.{table}.fields.{field_name}.{key}", f"unknown reference {value!r}")

    all_dataset_refs = set(expected_assets) | identifiers["upstream_datasets"]
    for index, node in enumerate(doc["glossary_nodes"]):
        if node.get("parent") and node["parent"] not in identifiers["glossary_nodes"]:
            _fail(f"metadata.glossary_nodes[{index}].parent", f"unknown glossary node reference {node['parent']!r}")
    for index, term in enumerate(doc["glossary_terms"]):
        if term.get("node") and term["node"] not in identifiers["glossary_nodes"]:
            _fail(f"metadata.glossary_terms[{index}].node", f"unknown glossary node reference {term['node']!r}")
    for table, asset in assets.items():
        if asset.get("certification") == "CERTIFIED" and "Certified" not in identifiers["tags"]:
            _fail(f"metadata.assets.{table}.certification", "requires a tags definition with id 'Certified'")
        replacement = asset.get("deprecation", {}).get("replacement")
        if replacement is not None:
            _nonempty_string(replacement, f"metadata.assets.{table}.deprecation.replacement")
    for index, job in enumerate(doc["transformation_jobs"]):
        for key in ("inputs", "outputs"):
            for value in job[key]:
                if value not in all_dataset_refs:
                    _fail(f"metadata.transformation_jobs[{index}].{key}", f"unknown dataset reference {value!r}")
        for owner in job.get("owners", []):
            if owner not in identifiers["groups"]:
                _fail(f"metadata.transformation_jobs[{index}].owners", f"unknown group reference {owner!r}")
    return result


def load_metadata(path: Path) -> dict[str, Any]:
    text = path.read_text(encoding="utf-8")
    try:
        import yaml  # supplied by the pinned acryl-datahub dependency set

        raw = yaml.safe_load(text)
    except ModuleNotFoundError:
        try:
            raw = json.loads(text)
        except json.JSONDecodeError as exc:
            raise MetadataValidationError("PyYAML is required to read non-JSON YAML") from exc
    except Exception as exc:
        raise MetadataValidationError(f"cannot parse {path}: {exc}") from exc
    return validate_metadata(raw)


def _dataset_name_from_urn(urn: str) -> str:
    """Extract the dataset name from an actual URN, including nested platform URNs."""
    prefix = "urn:li:dataset:("
    suffix = ",DEV)"
    if not urn.startswith(prefix) or not urn.endswith(")"):
        raise DiscoveryError(f"search returned malformed dataset URN: {urn!r}")
    body = urn[len(prefix) : -1]
    marker = ")," if body.startswith("urn:li:dataPlatformInstance:(") else ","
    platform_end = body.find(marker)
    if platform_end < 0:
        raise DiscoveryError(f"search returned malformed dataset URN: {urn!r}")
    remainder = body[platform_end + len(marker) :]
    name, separator, _env = remainder.rpartition(",")
    if not separator or not name:
        raise DiscoveryError(f"search returned malformed dataset URN: {urn!r}")
    return name


def discover_manifest(
    client: DiscoveryClient,
    expected_assets: Sequence[str],
    *,
    platform: str,
    platform_instance: str,
    env: str,
    database: str,
) -> dict[str, Any]:
    """Resolve every expected table from actual search results, never constructed URNs."""
    candidates = sorted(set(client.list_dataset_urns(
        platform=platform,
        platform_instance=platform_instance,
        env=env,
        query=database,
    )))
    resolved: dict[str, str] = {}
    missing: list[str] = []
    for table in expected_assets:
        qualified = f"{database}.{table}".lower()
        matches = []
        for urn in candidates:
            name = _dataset_name_from_urn(urn).lower()
            if name == qualified or name.endswith("." + qualified):
                matches.append(urn)
        if not matches:
            missing.append(f"{database}.{table}")
            continue
        if len(matches) > 1:
            raise DiscoveryError(
                f"ambiguous DataHub asset for {database}.{table}: " + ", ".join(matches)
            )
        resolved[table] = matches[0]
    if missing:
        raise MissingAssetsError(
            f"missing DataHub assets: {', '.join(missing)} "
            f"(platform={platform}, instance={platform_instance}, env={env})"
        )
    return {
        "schema_version": MANIFEST_SCHEMA_VERSION,
        "platform": platform,
        "platform_instance": platform_instance,
        "env": env,
        "database": database,
        "assets": dict(sorted(resolved.items())),
    }


def discover_manifest_with_retry(
    client: DiscoveryClient,
    expected_assets: Sequence[str],
    *,
    platform: str,
    platform_instance: str,
    env: str,
    database: str,
    timeout_seconds: float,
    interval_seconds: float,
    logger: logging.Logger,
) -> dict[str, Any]:
    """Wait for ingestion-created assets to become visible in DataHub search."""
    deadline = time.monotonic() + timeout_seconds
    attempts = 0
    while True:
        attempts += 1
        try:
            return discover_manifest(
                client,
                expected_assets,
                platform=platform,
                platform_instance=platform_instance,
                env=env,
                database=database,
            )
        except MissingAssetsError as exc:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise DiscoveryError(
                    f"{exc}; assets did not become visible within "
                    f"{timeout_seconds:g} seconds after {attempts} attempts"
                ) from exc
            delay = min(interval_seconds, remaining)
            logger.info(
                "DataHub search has not indexed all datasets; "
                "retrying in %.1f seconds: %s",
                delay,
                exc,
            )
            time.sleep(delay)


def write_manifest(manifest: Mapping[str, Any], output: Path | None = None) -> str:
    serialized = json.dumps(manifest, sort_keys=True, separators=(",", ":"))
    if output is not None:
        output.write_text(serialized + "\n", encoding="utf-8")
    return serialized


def build_plan(metadata: Mapping[str, Any], manifest: Mapping[str, Any]) -> list[Operation]:
    """Build a stable convergence plan. Rebuilding it never accumulates operations."""
    operations: list[Operation] = []
    for definition in FIXED_PROPERTIES:
        operations.append(Operation("property_definition", definition["id"], definition["id"], definition))
    section_kinds = (
        ("domains", "domain"),
        ("groups", "group"),
        ("glossary_nodes", "glossary_node"),
        ("glossary_terms", "glossary_term"),
        ("tags", "tag"),
        ("upstream_datasets", "upstream_dataset"),
        ("transformation_jobs", "transformation_job"),
    )
    for section, kind in section_kinds:
        for item in sorted(metadata[section], key=lambda value: value["id"]):
            operations.append(Operation(kind, f"{kind}:{item['id']}", item["id"], item))
    for table, asset in sorted(metadata["assets"].items()):
        urn = manifest["assets"].get(table)
        if urn is None:
            raise DiscoveryError(f"manifest has no discovered URN for configured asset {table!r}")
        payload = dict(asset)
        payload["table"] = table
        operations.append(Operation("asset_patch", f"asset:{table}", urn, payload))
    keys = [operation.key for operation in operations]
    if len(keys) != len(set(keys)):
        raise MetadataValidationError("internal plan contains duplicate operation keys")
    return operations


class DataHubClient:
    """Thin adapter around the exact SDK APIs shipped by acryl-datahub 1.2.0.10."""

    def __init__(self, server: str, token: str) -> None:
        from datahub.ingestion.graph.client import DataHubGraph
        from datahub.ingestion.graph.config import DatahubClientConfig

        self.graph = DataHubGraph(DatahubClientConfig(server=server, token=token))
        self._manifest: Mapping[str, Any] = {}
        self._metadata: Mapping[str, Any] = {}

    def list_dataset_urns(
        self, *, platform: str, platform_instance: str, env: str, query: str
    ) -> Iterable[str]:
        return self.graph.get_urns_by_filter(
            entity_types=["dataset"],
            platform=platform,
            platform_instance=platform_instance,
            env=env,
            query=query,
            skip_cache=True,
        )

    def bind(self, metadata: Mapping[str, Any], manifest: Mapping[str, Any]) -> None:
        self._metadata = metadata
        self._manifest = manifest

    @staticmethod
    def _urn(kind: str, identifier: str) -> str:
        prefixes = {
            "domain": "urn:li:domain:",
            "group": "urn:li:corpGroup:",
            "glossary_node": "urn:li:glossaryNode:",
            "glossary_term": "urn:li:glossaryTerm:",
            "tag": "urn:li:tag:",
        }
        return prefixes[kind] + identifier

    def _dataset_refs(self) -> dict[str, str]:
        from datahub.emitter.mce_builder import make_dataset_urn_with_platform_instance

        refs = dict(self._manifest["assets"])
        for item in self._metadata["upstream_datasets"]:
            refs[item["id"]] = make_dataset_urn_with_platform_instance(
                platform=item.get("platform", "external-data"),
                name=item["name"],
                platform_instance=item.get("platform_instance"),
                env=item.get("env", DEFAULT_ENV),
            )
        return refs

    def apply(self, operation: Operation) -> None:
        from datahub.api.entities.corpgroup.corpgroup import CorpGroup
        from datahub.api.entities.datajob.dataflow import DataFlow
        from datahub.api.entities.datajob.datajob import DataJob
        from datahub.api.entities.structuredproperties.structuredproperties import StructuredProperties
        from datahub.emitter.mce_builder import make_dataset_urn_with_platform_instance, make_group_urn
        from datahub.emitter.mcp import MetadataChangeProposalWrapper
        from datahub.metadata.schema_classes import (
            AuditStampClass,
            DatasetLineageTypeClass,
            DatasetPropertiesClass,
            DeprecationClass,
            DomainsClass,
            DomainPropertiesClass,
            EditableSchemaFieldInfoClass,
            EditableSchemaMetadataClass,
            GlobalTagsClass,
            GlossaryNodeInfoClass,
            GlossaryTermAssociationClass,
            GlossaryTermInfoClass,
            GlossaryTermsClass,
            OwnerClass,
            OwnershipTypeClass,
            StatusClass,
            TagAssociationClass,
            TagPropertiesClass,
            UpstreamClass,
        )
        from datahub.metadata.urns import DataFlowUrn, DatasetUrn
        from datahub.specific.dataset import DatasetPatchBuilder

        payload = dict(operation.payload)
        mcps: list[Any] = []
        if operation.kind == "property_definition":
            entity = StructuredProperties(
                id=payload["id"],
                type="string",
                display_name=payload["display_name"],
                description="SaaS account semantic linkage property.",
                entity_types=["dataset"],
                cardinality=payload["cardinality"],
            )
            mcps.extend(entity.generate_mcps())
        elif operation.kind == "domain":
            mcps.extend([
                MetadataChangeProposalWrapper(entityUrn=self._urn("domain", operation.target), aspect=DomainPropertiesClass(name=payload["name"], description=payload.get("description"))),
            ])
        elif operation.kind == "group":
            mcps.extend(CorpGroup(id=operation.target, display_name=payload["name"], description=payload.get("description"), email=payload.get("email")).generate_mcp())
        elif operation.kind == "glossary_node":
            mcps.extend([
                MetadataChangeProposalWrapper(entityUrn=self._urn("glossary_node", operation.target), aspect=GlossaryNodeInfoClass(definition=payload.get("description", payload["name"]), name=payload["name"], id=operation.target, parentNode=self._urn("glossary_node", payload["parent"]) if payload.get("parent") else None)),
            ])
        elif operation.kind == "glossary_term":
            mcps.extend([
                MetadataChangeProposalWrapper(entityUrn=self._urn("glossary_term", operation.target), aspect=GlossaryTermInfoClass(definition=payload["definition"], termSource="INTERNAL", id=operation.target, name=payload["name"], parentNode=self._urn("glossary_node", payload["node"]) if payload.get("node") else None)),
            ])
        elif operation.kind == "tag":
            mcps.extend([
                MetadataChangeProposalWrapper(entityUrn=self._urn("tag", operation.target), aspect=TagPropertiesClass(name=payload["name"], description=payload.get("description"), colorHex=payload.get("color"))),
            ])
        elif operation.kind == "upstream_dataset":
            urn = make_dataset_urn_with_platform_instance(payload.get("platform", "external-data"), payload["name"], payload.get("platform_instance"), payload.get("env", DEFAULT_ENV))
            mcps.extend([
                MetadataChangeProposalWrapper(entityUrn=urn, aspect=DatasetPropertiesClass(name=payload["name"], description=payload.get("description"), customProperties={})),
                MetadataChangeProposalWrapper(entityUrn=urn, aspect=StatusClass(removed=False)),
            ])
        elif operation.kind == "transformation_job":
            refs = self._dataset_refs()
            flow = DataFlow(id=payload["flow_id"], orchestrator="semantic-operator", env=DEFAULT_ENV, name=payload["flow_id"], description="SaaS account metadata transformation flow")
            mcps.extend(flow.generate_mcp())
            job = DataJob(
                id=payload["id"],
                flow_urn=DataFlowUrn.from_string(str(flow.urn)),
                name=payload["name"],
                description=payload.get("description"),
                group_owners=set(payload.get("owners", [])),
                inlets=[DatasetUrn.from_string(refs[value]) for value in payload["inputs"]],
                outlets=[DatasetUrn.from_string(refs[value]) for value in payload["outputs"]],
            )
            mcps.extend(job.generate_mcp())
        elif operation.kind == "asset_patch":
            builder = DatasetPatchBuilder(operation.target)
            if payload.get("documentation"):
                builder.set_description(payload["documentation"])
            if payload.get("domain"):
                mcps.append(MetadataChangeProposalWrapper(
                    entityUrn=operation.target,
                    aspect=DomainsClass(domains=[self._urn("domain", payload["domain"])]),
                ))
            for owner in payload.get("owners", []):
                builder.add_owner(OwnerClass(owner=make_group_urn(owner), type=OwnershipTypeClass.TECHNICAL_OWNER))
            for term in payload.get("glossary_terms", []):
                builder.add_term(GlossaryTermAssociationClass(urn=self._urn("glossary_term", term)))
            tags = list(payload.get("tags", []))
            if payload.get("certification") == "CERTIFIED":
                tags.append("Certified")
            elif payload.get("certification") == "UNVERIFIED":
                builder.remove_tag(self._urn("tag", "Certified"))
            for tag in sorted(set(tags)):
                builder.add_tag(TagAssociationClass(tag=self._urn("tag", tag)))
            for prop, value in sorted(payload.get("structured_properties", {}).items()):
                builder.set_structured_property(f"urn:li:structuredProperty:{prop}", value)
            fields = payload.get("fields", {})
            if fields:
                audit_stamp = AuditStampClass(time=0, actor="urn:li:corpuser:datahub")
                field_info = []
                for field_name, field in sorted(fields.items()):
                    tags = sorted(set(field.get("tags", [])))
                    terms = sorted(set(field.get("glossary_terms", [])))
                    field_info.append(EditableSchemaFieldInfoClass(
                        fieldPath=field_name,
                        description=field.get("description"),
                        globalTags=GlobalTagsClass(tags=[
                            TagAssociationClass(tag=self._urn("tag", tag))
                            for tag in tags
                        ]) if tags else None,
                        glossaryTerms=GlossaryTermsClass(
                            terms=[
                                GlossaryTermAssociationClass(
                                    urn=self._urn("glossary_term", term),
                                )
                                for term in terms
                            ],
                            auditStamp=audit_stamp,
                        ) if terms else None,
                    ))
                mcps.append(MetadataChangeProposalWrapper(
                    entityUrn=operation.target,
                    aspect=EditableSchemaMetadataClass(
                        editableSchemaFieldInfo=field_info,
                        created=audit_stamp,
                        lastModified=audit_stamp,
                    ),
                ))
            refs = self._dataset_refs()
            for upstream in payload.get("upstreams", []):
                builder.add_upstream_lineage(UpstreamClass(dataset=refs[upstream], type=DatasetLineageTypeClass.TRANSFORMED))
            mcps.extend(builder.build())
            if "deprecation" in payload:
                dep = payload["deprecation"]
                mcps.append(MetadataChangeProposalWrapper(entityUrn=operation.target, aspect=DeprecationClass(deprecated=dep["deprecated"], note=dep["note"], actor="urn:li:corpuser:datahub", replacement=dep.get("replacement"))))
        else:
            raise ApplyError(f"unsupported operation kind {operation.kind!r}")

        try:
            for mcp in mcps:
                self.graph.emit_mcp(mcp)
        except Exception as exc:
            raise ApplyError(
                f"DataHub rejected {operation.kind} operation {operation.key!r} "
                f"for {operation.target!r}: {exc}"
            ) from exc


def apply_plan(client: Any, operations: Sequence[Operation]) -> None:
    for operation in operations:
        client.apply(operation)


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--metadata", type=Path, required=True)
    parser.add_argument("--manifest-output", type=Path)
    parser.add_argument("--server", default=os.getenv("DATAHUB_GMS_URL"))
    parser.add_argument("--token", default=os.getenv("DATAHUB_GMS_TOKEN"))
    parser.add_argument("--platform", default=DEFAULT_PLATFORM)
    parser.add_argument("--platform-instance", default=os.getenv("DATAHUB_PLATFORM_INSTANCE"))
    parser.add_argument("--env", default=DEFAULT_ENV)
    parser.add_argument("--database", default=DEFAULT_DATABASE)
    parser.add_argument(
        "--discovery-timeout-seconds",
        type=float,
        default=DEFAULT_DISCOVERY_TIMEOUT_SECONDS,
        help="maximum time to wait for ingested datasets to appear in search",
    )
    parser.add_argument(
        "--discovery-interval-seconds",
        type=float,
        default=DEFAULT_DISCOVERY_INTERVAL_SECONDS,
        help="delay between missing-dataset discovery attempts",
    )
    parser.add_argument("--dry-run", action="store_true", help="discover and plan without writing aspects")
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    logger = configure_logging(args.token)
    try:
        if not args.server:
            raise EnrichmentError("DATAHUB_GMS_URL or --server is required")
        if not args.token:
            raise EnrichmentError("DATAHUB_GMS_TOKEN or --token is required")
        if not args.platform_instance:
            raise EnrichmentError("DATAHUB_PLATFORM_INSTANCE or --platform-instance is required")
        if args.discovery_timeout_seconds < 0:
            raise EnrichmentError("--discovery-timeout-seconds must be zero or greater")
        if args.discovery_interval_seconds <= 0:
            raise EnrichmentError("--discovery-interval-seconds must be greater than zero")
        metadata = load_metadata(args.metadata)
        client = DataHubClient(args.server, args.token)
        manifest = discover_manifest_with_retry(
            client,
            metadata["expected_assets"],
            platform=args.platform,
            platform_instance=args.platform_instance,
            env=args.env,
            database=args.database,
            timeout_seconds=args.discovery_timeout_seconds,
            interval_seconds=args.discovery_interval_seconds,
            logger=logger,
        )
        manifest_json = write_manifest(manifest, args.manifest_output)
        print(manifest_json, flush=True)
        plan = build_plan(metadata, manifest)
        if args.dry_run:
            logger.info("dry-run plan contains %d convergent operations", len(plan))
            return 0
        client.bind(metadata, manifest)
        apply_plan(client, plan)
        logger.info("enrichment applied %d convergent operations", len(plan))
        return 0
    except EnrichmentError as exc:
        logger.error("%s", exc)
        return 1
    except Exception as exc:
        logger.error("unexpected enrichment failure: %s", exc)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
