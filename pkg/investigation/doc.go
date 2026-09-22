// Package investigation is the contract between Radar's AI investigation
// agents and the Findings page: what the model is asked for, what it may
// answer, how that answer is read, and how its citations are bound to the tool
// results Radar recorded.
//
// Two products drive an agent and render the same page. Radar OSS runs a
// coding CLI on the user's machine; Radar Cloud runs the Claude Agent SDK in a
// sandboxed Job. Each owns its run lifecycle, transport and store. What they
// share is here, so a change to the prompt, the verdict shape, a cap or a
// binding rule lands once and reaches both when they update this module.
//
// The package is pure: no process, no network, no storage. It has four parts.
//
//   - The prompt suite (prompt.go): the system prompt, the task, follow-up,
//     verification, apply and explanation prompts, and the tool allowlists.
//   - The verdict (verdict.go): the parsed shape the page renders, its caps,
//     and the rules that decide whether a turn is an assessment.
//   - Parsing (parse.go): Parse reads the model's final text into a Verdict
//     plus the untrusted citations it asked for.
//   - Binding (bind.go): Bind promotes those citations to server-authored
//     provenance against an eligibility decision the product supplies, since
//     only the party that issued a ref can say whether it stands.
//
// Evidence refs (refs.go) name one recorded tool result. Their grammar is
// shared so a ref minted by either product parses the same way; issuance and
// validation stay with the product, which is the credential-bearing party.
package investigation
