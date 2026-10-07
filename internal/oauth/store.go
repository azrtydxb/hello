package oauth

// Store is the authorization server's persistence, implemented by
// *store.Store over the 00008_ai_access tables.
//
// Contract only: the client, request, grant, token and secret operations
// are written by the lead with plan ai-external-access Task 3's first
// commit and fixed thereafter.
type Store interface{}
