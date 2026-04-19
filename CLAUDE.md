# Release discipline

Never create or push a git tag unless the human explicitly asks for one in
the current turn. Earlier approvals to tag (e.g. "tag v0.2.0") do not
authorize follow-up tags — each release tag is its own explicit request.

A user saying "commit and push" does not imply tagging. A user saying
"release" or "cut a release" does imply a tag, but confirm the version
number before pushing.
