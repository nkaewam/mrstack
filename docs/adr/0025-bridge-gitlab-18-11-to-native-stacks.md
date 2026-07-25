# Bridge GitLab 18.11 to native stacks

The CLI exists because Agoda GitLab 18.11 predates GitLab 19.1's native stack detection and automatic successor retargeting, with the organizational upgrade expected next quarter. V1 therefore implements the missing relationship discovery and post-merge advancement over ordinary merge requests without invoking the experimental `glab stack` workflow; native 19.1 behavior is the migration boundary, not a current dependency.
