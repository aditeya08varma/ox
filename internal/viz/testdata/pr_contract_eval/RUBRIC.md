# PR visual-contract evaluation rubric

The corpus uses real merged SageOx PRs with substantial reviewer discussion.
The baseline is the visual that shipped in the PR body (or the closest visual
already present there), not a newly sampled AI output. The candidate is built
from the same reviewer question and evidence after `ox viz` supplies a visual
contract.

Score each dimension from 0–10 at the exported image's intended 50% display
scale. Review the technical claim before judging polish.

| Dimension | Weight | Question |
|---|---:|---|
| Reviewer answerability | 25% | Can a reviewer answer the named question within five seconds? |
| Conceptual clarity | 20% | Does the spatial model match the human mental model—obvious start, primary path, changed fact, and outcome—without reconstructing relationships across decorative groups? |
| Semantic fidelity | 20% | Does every visible claim follow from the PR evidence without invented state, timing, grouping, or causality? |
| Visual hierarchy | 10% | Is the conclusion dominant, with one focal change and appropriately quiet context? |
| Spatial integrity | 10% | Do connectors terminate correctly, labels avoid collisions, and geometry encode the intended relationship? |
| Typography | 5% | Is every required label legible at 50% without awkward wrapping or inconsistent scale? |
| Cognitive economy | 10% | Does every mark earn its place, and is this simpler than the next smaller truthful medium? |

The weighted score is the sum of `score × weight`. A candidate passes only if:

- weighted score is at least 8.0;
- conceptual clarity is at least 8;
- semantic fidelity is at least 8;
- spatial integrity is at least 8;
- no required evidence slot is missing;
- the simple-change control does not get promoted from Mermaid to a rich image.

Every candidate is scored against the smallest truthful baseline that answers
the same reviewer question. A rich candidate fails—regardless of polish or
weighted average—when the baseline makes the start, primary path, changed fact,
or outcome faster to identify. In that case, keep the baseline and improve only
its typography, spacing, direction, and restrained emphasis.

Mechanical lint is necessary but does not affect the aesthetic score. Any text
collision, disconnected connector, false scale, missing endpoint, or misleading
encoding is a release blocker regardless of the weighted average.
