package messaging

// AssignOutcome is the result of offering a waiting conversation to one agent.
type AssignOutcome int

// The outcomes of an assignment attempt.
const (
	// AssignDone: the conversation now belongs to the agent.
	AssignDone AssignOutcome = iota
	// AssignFull: the agent already holds as many conversations as the queue
	// allows (someone else filled the last place first); try the next agent.
	AssignFull
	// AssignGone: the conversation is no longer waiting (an agent took it, it
	// was resolved or moved); stop.
	AssignGone
)
