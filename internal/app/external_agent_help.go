package app

const externalMCPHelpMethod = "mcp"

func (a *ExternalAgent) externalMCPHelp(session externalSession) helpCard {
	card := helpCard{
		Summary: "Get started with Loom MCP",
		Text: "# Loom MCP\n\n" +
			"`script` runs read-only JavaScript. Pass `code`, await module calls, then return JSON.\n\n" +
			"- Find reads with `return await modules.help.list({query: 'booking'});`. " +
			"If the list is partial, pass `next_cursor` as `after_id` with the same filters.\n" +
			"- Get a method's contract and call syntax with `modules.help.read({module, method})`. " +
			"Call it with `await modules.<module>.<method>(arguments)`.\n" +
			"- Find procedures with `modules.skills.list({})`, then use `modules.skills.read({name, source})`.\n" +
			"- When multiple token services are offered, select `token_service` on the MCP tool call.\n",
	}
	if !session.profile.Writes || len(a.availableTokenServices()) == 0 {
		return card
	}
	card.Text += "\n`write` starts or continues an operation.\n\n" +
		"1. In `script`, run `return await modules.help.write_catalog({});` to find actions and their input schemas. " +
		"If multiple token services are offered, pass `token_service` to `script` to expose the write helpers. " +
		"Pass `next_offset` as `offset` to continue the list.\n" +
		"2. Call `write` with `module`, `method`, `arguments` and a new UUID `request_id`. " +
		"If the response is lost, reuse the same ID and arguments.\n" +
		"3. If it returns `waiting_approval`, ask the user to confirm in Telegram. " +
		"Call `write` with the same `operation_id` to continue after approval or retry delivery of a missing prompt.\n" +
		"4. In `script`, read a receipt with `modules.help.write_status({operation_id})`. " +
		"Use the original token service for all calls about the operation.\n"
	return card
}
