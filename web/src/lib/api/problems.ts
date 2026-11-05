export interface ApiProblem {
	type: string;
	title: string;
	status: number;
	detail?: string;
	code: string;
	fields?: Record<string, string>;
}

const FALLBACK_CODE = 'internal_error';

function fallbackProblem(response: Response): ApiProblem {
	return {
		type: 'about:blank',
		title: response.statusText || 'Request failed',
		status: response.status,
		code: FALLBACK_CODE,
	};
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === 'object' && value !== null;
}

export function isProblemBody(value: unknown): boolean {
	return isRecord(value) && typeof value.code === 'string';
}

function parseFields(value: unknown): Record<string, string> | undefined {
	if (!isRecord(value)) return undefined;
	const fields: Record<string, string> = {};
	for (const [key, message] of Object.entries(value)) {
		if (typeof message === 'string') fields[key] = message;
	}
	return Object.keys(fields).length > 0 ? fields : undefined;
}

export async function parseProblem(response: Response): Promise<ApiProblem> {
	let body: unknown;
	try {
		body = await response.json();
	} catch {
		return fallbackProblem(response);
	}

	if (!isProblemBody(body)) return fallbackProblem(response);

	const problem = body as Record<string, unknown>;
	const fields = parseFields(problem.fields);

	return {
		type: typeof problem.type === 'string' ? problem.type : 'about:blank',
		title:
			typeof problem.title === 'string'
				? problem.title
				: response.statusText || 'Request failed',
		status:
			typeof problem.status === 'number' ? problem.status : response.status,
		...(typeof problem.detail === 'string' ? { detail: problem.detail } : {}),
		code: problem.code as string,
		...(fields ? { fields } : {}),
	};
}
