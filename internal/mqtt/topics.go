package mqtt

const (
	// TopicArticlesRaw is where source primitives publish. Downstream primitives don't care about the source.
	TopicArticlesRaw = "minerva/articles/raw"

	// TopicArticlesExtracted is where the extractor publishes clean text.
	TopicArticlesExtracted = "minerva/articles/extracted"

	// TopicArticlesAnalyzed is where the Ollama analyzer publishes LLM analysis.
	// Content field is dropped at this stage — it's large and no longer needed downstream.
	TopicArticlesAnalyzed = "minerva/articles/analyzed"

	// TopicBooksCandidates is where the OpenLibrary primitive publishes book candidates.
	TopicBooksCandidates = "minerva/books/candidates"

	// TopicBooksChecked is where the Koha primitive publishes ownership-checked books.
	TopicBooksChecked = "minerva/books/checked"

	// TopicArticlesComplete is where the notifier publishes after successful notification.
	// Source primitives subscribe to this for completion tracking.
	TopicArticlesComplete = "minerva/articles/complete"

	// TopicPipelineTrigger is the external trigger topic.
	// Source primitives subscribe here — Nomad batch job or manual run publishes here.
	TopicPipelineTrigger = "minerva/pipeline/trigger"
)
