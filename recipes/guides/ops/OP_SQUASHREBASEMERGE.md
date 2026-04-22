# squashrebasemerge Op

Squashes local commits, rebases onto the latest upstream branch, and fast-forward merges back to the remote.

## Input Structure

```json
{
  "repo_path": "/path/to/repo",
  "local_hash": "abc1234",
  "upstream_repo": "git@example.com:org/repo.git",
  "upstream_branch": "main",
  "rebase": true,
  "author": "Jane Doe <jane@example.com>",
  "commit_message": "feat: update change"
}
```

## Output Structure

```json
{
  "target_branch": "main",
  "remote_ref": "refs/heads/main",
  "merged_hash": "0123abc",
  "squashed_commits": {
    "base_hash": "def5678",
    "persist_hash": "fedcba9"
  },
  "git_context_patch": {},
  "fast_forward": true
}
```
