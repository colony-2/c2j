# thinpackrebase Op

Rebases a thin-pack backed workspace onto a new base commit and returns updated git metadata for downstream ops.

## Input Structure

```json
{
  "repo_path": "/path/to/repo",
  "target_base_hash": "abc1234",
  "upstream_remote": "origin",
  "preserve_author": true,
  "update_refs": "refs/heads/main",
  "base_hash": "def5678",
  "persist_hash": "fedcba9",
  "base_repo": "git@example.com:org/repo.git",
  "git_author": "Jane Doe <jane@example.com>",
  "cell_name": "api"
}
```

## Output Structure

```json
{
  "target_base_hash": "abc1234",
  "new_base_hash": "0123abc",
  "new_persist_hash": "4567def",
  "updated_ref": "refs/heads/main",
  "rebased_from": {
    "base_hash": "def5678",
    "persist_hash": "fedcba9"
  },
  "git_context_patch": {}
}
```
