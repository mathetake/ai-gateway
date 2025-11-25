package dynamic_module

// endpoint represents the type of the endpoint that the request is targeting.
type endpoint int

const (
	// chatCompletionsEndpoint represents the /v1/chat/completions endpoint.
	chatCompletionsEndpoint endpoint = iota
	// completionsEndpoint represents the /v1/completions endpoint.
	completionsEndpoint
	// embeddingsEndpoint represents the /v1/embeddings endpoint.
	embeddingsEndpoint
	// imagesGenerationsEndpoint represents the /v1/images/generations endpoint.
	imagesGenerationsEndpoint
	// rerankEndpoint represents the /v2/rerank endpoint of cohere.
	rerankEndpoint
	// messagesEndpoint represents the /v1/messages endpoint of anthropic.
	messagesEndpoint
)
