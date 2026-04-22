# Recipe Test Cases Plan (Index)

This plan is split into three implementation docs so teams can work in parallel with clear ownership:

1. Start here (overview for agents):
   - `guides/recipe-testing-specs/OVERVIEW.md`
2. CLI requirements and behavior:
   - `guides/recipe-testing-specs/RECIPE_TESTING_CLI_SPEC.md`
3. API contract / OpenAPI updates:
   - `guides/recipe-testing-specs/RECIPE_TESTING_OPENAPI_UPDATES_SPEC.md`
4. Server runtime behavior requirements:
   - `guides/recipe-testing-specs/RECIPE_TESTING_SERVER_BEHAVIOR_REQUIREMENTS.md`

## Coordination contract

If each team implements its document:

1. CLI compiles human-authored suites and orchestrates per-case execution (including parallelism).
2. API/server contract supports per-case validate/execute requests only.
3. Server returns full per-case outcomes without persisting run state.

## Important shared decisions

1. No server-side storage of reusable test suite definitions.
2. No server-side persisted test-run objects or run-report lookup endpoints.
3. Test operations support both:
   - local provided inline recipe content
   - server recipe references (name + version/ref)
4. CLI reports case outcomes as they occur and writes local reports/artifacts.
5. Cases support outcome evaluations over outputs/artifacts/traces, including text-pattern and LLM-judge evaluators.
