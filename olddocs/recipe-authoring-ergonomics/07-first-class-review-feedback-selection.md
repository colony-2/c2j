# Requirement 7: First-Class Review Feedback Selection

## Does The Spirit Make Sense?

Yes as a target authoring pattern, not as a separate workflow-specific feature. The requirement correctly avoids hard-coded "review feedback" helpers.

## Proposal

- Implement this through requirements 1, 2, and 4:
  - transition payloads carry target-specific feedback from the source review state;
  - `transition.?payload.user_feedback.orValue("")` reads the preferred value;
  - recipes should not fall back to prior review states for review feedback selection.
- Encourage recipes to put feedback selection policy at the transition source whenever the source knows the destination.
- Do not define an implicit winner across multiple prior review states. Transition payloads are the only supported review-feedback selection mechanism for this pattern.

## Risks

- If a transition omits payload feedback, the target will not receive review feedback through this pattern.
- This is a hard authoring break for recipes that currently scan prior review states.
- Whitespace-only feedback and duplicated review states can still produce surprising choices unless semantics are documented.
- `first_nonempty(...)` trims strings only for the emptiness check; selected feedback values are returned unchanged.

## Clarifying Questions

Resolved.

## Decisions

- There is no implicit winner across multiple prior review states.
- Transition payloads are the only supported review-feedback selection mechanism for this pattern.
- Do not maintain compatibility fallback to prior review states; this is an acceptable hard break.
- Feedback strings are trimmed only for emptiness checks; selected values are returned unchanged.
