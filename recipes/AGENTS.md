## Purpose
You are an agent responsible for implementing and maintaining recipes for the c2 software development system.

## C2 Concepts
c2 is used for completing tasks related to building and running software. c2 can be used to build anything from simple tools (a bash script) to complex cloud-run multi-component, multi-tenant services. c2 is responsible for creating, validating, debugging software. For projects that need to be run in a cloud provider, C2 is also responsible for , provisioning infrastructure, deploying code, upgrading software and systems and monitoring using tools like terraform and hyperscalers like AWS.

c2 is designed to be an adaptive, extensible system that is built using the following primitives:

- actor: any entity working to move a c2 project forward. an entity may be an llm-based agent, a human or some kind of external process.
- project: a specific software initiative. May be an operationally complex SaaS service or a simple CLI tool. Each project is associated with a specific git repository (subcomponents, what c2 may be in other repositories).
- cell: a context for a specific task. Cells are designed to decompose a software project into distinct, manageable chunks/components.
- recipe: a recipe is a c2 workflow that will be completed in a durable way. a recipe is composed of ops (operations), sequences and state machines. Recipes are versioned in c2 and can be saved or saved and published. When executing recipes, the published version of recipes are used. See guides/RECIPE_AUTHORING_GUIDE.md for more details
- op: an op or operation is the building block of a recipe. Ops are combined together to achieve arbitrary goals. Ops include things like consulting an llm, running a bash command, merging some git code into a repo, starting and/or consuming execution of other recipes, delegating to GitHub Actions workflows and having a coding agent build some code. Ops defined input and output structured types. See guides/ops/* for information about available ops, including guides/ops/OP_GITHUB_ACTIONS.md.
- template: recipes support the use of CEL-based template expansion and referencing of both global context and specific op/state/sequence outputs in a recipe. (see )
- job: a job is a specific execution of a recipe. Jobs can be triggered by tickets, other jobs or directly via the cli. The words job and workflow are often used interchangeably in c2. CLI direct execution is usually used for job authoring and debugging.
- job story: a job story is the description of what happened in a job. it allows introspection of a job's execution both while it is running and upon completion. A story describes the sequence of steps the c2 engine took including state machine decisions, inputs and outputs at each step as well as any artifacts used
- ticket: a ticket is a piece of work to be completed. a ticket may be created by a human or machine actor. tickets are always constrained to a single cell. Tickets automatically trigger an execution of a recipe. This is known as the primary job or workflow. The specific recipe triggered is either the value set on the cell or if no value set, the new ticket value set on the project

## Cells: Purpose and Parameters

Cells are a fundamental concept in c2.  Each artifact/line of code/etc of a project must belong to one and only one cell. As a project grows in complexity, the cell count for a project increases. Over time cells are added, cells subdivide, cells merge and cells are removed. Cell size is targeted to <10000 lines of code including production code, scripts, documentation, tests, etc. Each cell must have a clear mandate. Cells are responsible for ensuring they maintain their mandate by triaging incoming tasks against that mandate. A cells mandate can change but needs to go through an evaluation proces to do so. Cells serve several critical purposes:

1. Keep context size manageable to maximize effectiveness of work being done by agents
2. Create a natural checkpoint for broad-reaching changes, ensuring that architectural and design standards are maintained.
3. Keep changeset size and compatibility rules standard.
4. Provide a sandbox boundary to keep operations "in their lane", maintaining things like encapsulation.

Commits to any repository are constrained to a single cell at a time. This is by design, allowing many tasks to be run in parallel while avoiding rebase thrashing of large changesets.

## C2 workflows and state

## Op Execution Context
Operations are executed in a working directory that contains three subdirectories:
- inbox: a location where any input artifacts are made available for the operation
- outbox: a location where an operation can output files that will be included as part of the outcome of that operation
- git: holds the cell/op specific git repository (see below for details)

Ops are generally run in a sandbox environment where access to outside resources are limited.

### Git Persistence
Each cell has an associated git repository and relative path. A recipe running in that cell will get a local copy of the associated repository (similar to git worktree). After each recipe op, C2 automatically creates a local commit of all changes to the cell's relative path. These changes are made available as thin packs as well as git diffs. (Thinpacks are effectively git changesets with a connected hash.) This git state is automatically propagated to later ops.

A job is typically run in a given ref (e.g. the main branch). Until an op mutates the underlying codebase, the ref typically moves forward to stay up to date with the latest version of the underlying ref. Once an op makes changes to the git repository, a base commit is locked and the future commits are based on top of that. This git management is automatic and independent of the underlying repo for the project.

Once a workflow is satisfied with a commit (through human and/or machine decisisions), ops can be used to rebase, squash merge, etc a commit back to the base repository. There are a number of template context variables available related to git commits (both global as well as op specific).

### Artifacts

In addition to git state, each op can work with artifacts. Artifacts are arbitrary additional files that are available in the system. Some ops declare specific input or output artifacts. Other ops use the built in inbox/outbox pattern. When an op recives a general artifact input, those files are automatically made available in the inbox directory for that op. When an op writes a file or files to an outbox, those files are automatically persisted as part of the output of that op.

## C2 CLI
Your environment is configured with the c2 cli to work with recipes. The url and project are already configured. The c2 cli supports help flag to get help on individaul commands. c2 can be used to validate and update recipes as well as create new tickets and get the outcome and story of jobs. Published recipes are the ones that are used when referenced in new ticket configuration or the child recipe op (so be sure to update AND publish a recipe to use it). There is a c2 user guide in the guides directory as well.

Your environment is already configured to point to the colony2 server and correct project so you can use c2 commands.

You should start any work by reviewing the existing recipes. You can view them by running `c2 recipe list` and then `c2 recipe get <recipe-name`.


## Working on Recipes

- When working on recipes, work iteratively. Making small changes and testing them often is the best way to get feedback. Often it is useful to create testing recipes that can be used to test out a single op. 
- Be sure to review the scope rules in guides/NODE_SCOPE_SPEC.md. Often, recipe authors will be confused by the scope rules when trying to refer to outputs of other operations.
- Prefer to use real ops as opposed to random code scripts (e.g. python) where possible.
- If the repository already has a useful GitHub workflow, prefer `gha.run` or `gha.runs` over re-implementing the same automation in ad hoc shell scripts. See `guides/ops/OP_GITHUB_ACTIONS.md`.
- When working with the LLM2 operations, try to use output schema to simplify things. 
- When working with the user input operation, limit use of unstructured fields to the bare minimum. For example, if you want to ask a user if they approve something, use a radio or similar type of input field as opposed to a text field.
- It is important that the primary job is largely a orchestration job and delegates to other recipes for actual work. The primary job serves a special purpose because its completion defines how dependent jobs are executed. A primary job should have a lifecyle directly corresponding to the primary ticket: it runs while the ticket is open and when it completes, the ticket is closed. If the ticket is completed successfully, the primary job should complete successfully. If the job or ticket are cancelled, the primary job should also be cancelled, etc.


## Key Software Development Concepts
We are focused on a formal software development process. The key steps are:

- triage: determining the validity of a request, it's appropriateness to the current cell versus others
- requirements: iterative definition of the key outcomes/requirements
- design: defining the scope of the work, the architecture, the design, etc.
- outcome determination: defining the test statements, acceptance criteria, etc.
- implementation: iterating on the code/tests, responding to contrarian agent inspections, etc.
- merge/completion: incorporating the work into the codebase, closing the ticket, etc.

A key component of the c2 system is that human reviewers are focused on reviewing outcomes in human language, not code. The c2 workflows/process must be designed to support that. For example, the design step should produce a design document that can be reviewed by a human. The implementation step should produce test statements and acceptance criteria that can be reviewed by a human. The merge/completion step should produce a summary of the work done and the outcomes achieved that can be reviewed by a human.

The key artifacts at each step are specifications for the following step to complete. These artifacts should be stored as c2 artifacts. All c2 artifacts are associated with the step that generates them and referrable to future steps. 

### Key Concepts

### Test Statements
Test statements are a list of statements that can be used to test the outcome of the work. Test statements must be written in a way that is easy to understand and easy to validate. The rules for test statements are:

- **MUST** be written in markdown
- **MUST** limit test statements to 30 words or less
- **MUST** each test statement should be annotated with the relevant filename(s)
- **MUST** annotate each test statement with relative importance
- **MUST** annotate each test statement with unit or integration and any required dependencies (e.g. docker)
- **MUST** use business/expectation language rather than implementation details
- **MUST** include both positive and negative test cases for critical functionality
- **MUST** focus on integration points and avoid testing trivial getters/setters

Any time changes are made to the codebase, the test statements must be updated to reflect the new state of the codebase. Test statements are defined before implementation. Test statements that are modified/reviewed as part of a change should only be done in the case of a deprecation plan.

Test statements should be stored as c2 artifacts.

### Deprecation Workflow

When a feature is deprecated in a c2 project, it goes through two steps: (1) mark for deprecation but still supports and (2) removed from codebase. These must always be separate tickets and merge points. The deprecation plan must document how this is to be completed. The deprecation plan is a structured document that targets AI agents. This should be composed of:

- a ordered list of things that must be done.
- each list item should include: a concise llm to-be-consumed description of what changes should be made and a command that can be used to identify if this pattern exists within each cell.

The data should be structured as a json document

The deprecation workflow is responsible for taking a breaking change instruction and doing the following:
- for each deprecation step that must be done. identify each component that must have that change. for each component, have component owning agent apply that change.
- for any inflight changes that exist once all components for a deprecation step have been made, create a barrier that requires they will only be allowed to merge once they also do not match the deprecation step identifcation pattern
- after that item is done such that no components still have the problematic pattern, move to the next deprecation step and repeat
- the final deprecation step should be to eliminate any remaining deprecated code.

The document should be structured to ensure this pattern can be completed effectively.

Deprecation plans should be composed as c2 artifacts and additional tickets built on those artifacts. It's importan that those tickets have correct ticket dependencies against the marking of items as deprecated and their other required parents.
