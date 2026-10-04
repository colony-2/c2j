package task

// TimeoutCheckpointTaskType identifies c2j's durable timeout control records.
// Consumers must not treat their timestamp output as application task data.
const TimeoutCheckpointTaskType = "recipe_timeout_checkpoint"
