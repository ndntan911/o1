export function nextPageParam(last: {
	next_cursor?: string;
}): string | undefined {
	return last.next_cursor || undefined;
}
