# Requirement 4: Transition Payloads

## Does The Spirit Make Sense?

Yes. This directly fixes the structural problem where target states reconstruct routing intent by scanning prior states. The source transition knows why it routed, so it should pass that intent forward.

## Proposal

- Extend `recipe.Transition` with `Payload map[string]interface{} yaml:"payload,omitempty"`.
- Add `transition` to CEL/template data with `transition.payload` plus built-in mechanical metadata such as `transition.from` and `transition.to`.
- Change transition evaluation to return a decision object containing `to` and rendered `payload`, not just the next state string.
- Render payload values using the same rendering rules as normal recipe inputs, in the source state's visible scope. Prefer adding an explicit `outputs` variable for transition evaluation/payload rendering instead of relying only on the current string replacement from `outputs.` to `states.<state>.outputs.`.
- If a matched transition's payload fails to render, the transition evaluation fails before the target state starts.
- Allow payloads on `initial` transitions for consistency. If omitted, the first state receives an empty payload.
- Store the selected payload on the next state invocation context. Initial states and transitions without payload should receive an empty map.
- Keep business-specific metadata such as `reason` explicit in the authored payload unless a later requirement identifies a generic built-in reason field.
- Extend the state observer and job-story model so stories show selected transition payloads.
- Extend recipe-test diagnostics/assertions so tests can inspect payload values.

## Risks

- State re-entry must bind the payload to the specific invocation, not the state ID globally.
- Payload rendering may use extension functions; replay and story generation must evaluate them deterministically or avoid executing unsafe functions.
- Adding payloads to job stories can expose user-provided feedback or other sensitive values.
- Current transition diagnostics only record expression/result/to-state. The observer interface and story model need a backward-compatible migration.

## Clarifying Questions

Resolved.

## Decisions

- Allow payloads on `initial` transitions. Initial states without a payload receive an empty payload map.
- Payload values use the same rendering rules as normal recipe inputs and are rendered in the source state's visible scope.
- If a matched transition's payload fails to render, transition evaluation fails before the target state starts.
- Expose built-in transition metadata such as `transition.from` and `transition.to` alongside `transition.payload`.
- Keep business-specific fields such as `reason` authored in `transition.payload` unless we later define a generic built-in reason.
