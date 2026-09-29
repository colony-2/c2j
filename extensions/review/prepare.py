"""Selector extension: review policy stays outside c2j's input runtime."""
import hashlib
import json
from pathlib import Path
import sys


STORED_ARTIFACT = {
    "type": "object",
    "required": ["kind", "stored"],
    "additionalProperties": False,
    "properties": {
        "kind": {"const": "stored"},
        "name": {"type": "string", "minLength": 1},
        "stored": {
            "type": "object", "required": ["key"], "additionalProperties": False,
            "properties": {"key": {
                "type": "object",
                "required": ["jobId", "taskOrdinal", "name", "sizeBytes"],
                "additionalProperties": False,
                "properties": {
                    "jobId": {"type": "string", "minLength": 1},
                    "taskOrdinal": {"type": "integer", "minimum": 0},
                    "name": {"type": "string", "minLength": 1},
                    "sizeBytes": {"type": "integer", "minimum": -1},
                },
            }},
        },
    },
}


def prepare(inputs):
    spec = inputs["spec"]
    options = spec.get("document_options", {})
    unknown = set(options) - set(inputs["documents"])
    if unknown:
        raise ValueError(f"document options refer to unknown documents: {sorted(unknown)}")
    root = (Path(inputs["inbox"]) / "documents").resolve()
    documents = {}
    annotations = {}
    for doc_id, original in inputs["documents"].items():
        if not doc_id:
            raise ValueError("document IDs must not be empty")
        ref = original
        # Recipe artifact inputs also accept bare JobDB artifact keys.
        if "kind" not in ref and "jobId" in ref:
            ref = {"kind": "stored", "name": ref["name"], "stored": {"key": ref}}
        if ref.get("kind") != "stored":
            raise ValueError(f"{doc_id}: snapshot external documents as stored artifacts first")
        key = ref["stored"]["key"]
        name = ref.get("name") or key["name"]
        path = (root / name).resolve()
        if not path.is_relative_to(root) or path == root:
            raise ValueError(f"{doc_id}: document path must stay inside the bound inbox")
        data = path.read_bytes()
        data.decode("utf-8")
        opts = options.get(doc_id, {})
        media = opts.get("media_type", "text/markdown")
        policy = opts.get("annotation_policy", "criticmarkup" if media == "text/markdown" else "none")
        if policy == "criticmarkup" and media != "text/markdown":
            raise ValueError(f"{doc_id}: CriticMarkup requires Markdown")
        digest = hashlib.sha256(data).hexdigest()
        documents[doc_id] = {
            "title": opts.get("title", doc_id), "media_type": media,
            "artifact": ref, "sha256": digest, "annotation_policy": policy,
        }
        if policy == "criticmarkup":
            annotations[doc_id] = {
                "type": "object", "required": ["base_sha256", "format", "artifact"],
                "additionalProperties": False,
                "properties": {
                    "base_sha256": {"const": digest},
                    "format": {"const": "criticmarkup"},
                    "artifact": STORED_ARTIFACT,
                },
            }
    rules = []
    for decision, config in spec["decisions"].items():
        constraints = []
        if config.get("accepts_reviewed_content", False):
            constraints.append({"properties": {"annotations": {"maxProperties": 0}}})
        if config.get("feedback_required", False):
            constraints.append({"anyOf": [
                {"required": ["feedback"], "properties": {"feedback": {"pattern": r"\S"}}},
                {"required": ["annotations"], "properties": {"annotations": {"minProperties": 1}}},
            ]})
        if constraints:
            rules.append({"if": {"properties": {"decision": {"const": decision}}},
                          "then": {"allOf": constraints}})
    response_schema = {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object", "required": ["decision"], "additionalProperties": False,
        "properties": {
            "decision": {"enum": list(spec["decisions"])},
            "feedback": {"type": "string"},
            "annotations": {"type": "object", "properties": annotations, "additionalProperties": False},
        },
    }
    if rules:
        response_schema["allOf"] = rules
    request = {
        "schema": "colony.review/v1", "title": spec["title"],
        "summary_markdown": spec.get("summary_markdown", ""),
        "decisions": spec["decisions"], "documents": documents,
    }
    if "subject" in spec:
        request["subject"] = spec["subject"]
    return {"form": {"request": request, "response_schema": response_schema,
                     "presentation": {"type": "colony.review/v1"}}}


if __name__ == "__main__":
    try:
        json.dump({"output": prepare(json.load(sys.stdin))}, sys.stdout)
    except (ValueError, KeyError, OSError) as error:
        print(f"review preparation failed: {error}", file=sys.stderr)
        sys.exit(1)
