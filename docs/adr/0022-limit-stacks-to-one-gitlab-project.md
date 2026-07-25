# Limit stacks to one GitLab project

V1 stacks may contain only merge requests whose source and target branches belong to the same GitLab project. Fork-origin or cross-project members are `invalid/cross_project_member`, because atomic multi-ref publication, exact leases, branch cleanup, and deterministic target progression cannot be guaranteed across repositories.
