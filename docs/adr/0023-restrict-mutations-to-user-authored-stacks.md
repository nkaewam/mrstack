# Restrict mutations to user-authored stacks

Checks may inspect any valid same-project stack, but v1 permits restacking only when every active MR is authored by the user authenticated through `glab`. There is no override for foreign-authored members, preventing an unattended agent from rewriting another contributor's stack merely because branch permissions allow it.
