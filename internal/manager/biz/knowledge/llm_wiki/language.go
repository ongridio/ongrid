package llm_wiki

// sourceLanguageRule keeps every user-visible Wiki stage aligned with the
// language of the evidence instead of the language used by the prompt.
const sourceLanguageRule = `Write every human-readable field and sentence in the same language as the source material.
Do not translate the source into English or any other language.
When a page cites sources in multiple languages, use the dominant language of that page's cited source text.
Preserve code, commands, paths, identifiers, API names, and schema keys exactly.`
