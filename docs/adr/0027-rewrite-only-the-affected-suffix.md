# Rewrite only the affected suffix

A restack will locate the first stale boundary and rewrite only that layer and its successors in order, preserving every aligned branch below it. Movement of the base makes the whole active stack the affected suffix; movement of a mid-stack predecessor rewrites only the dependent layers above that boundary, minimizing force-pushes, pipeline churn, and review invalidation.
