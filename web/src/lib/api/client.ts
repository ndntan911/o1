import {
	clearWorkspaceSuspended,
	markWorkspaceSuspended,
} from '@/lib/workspace-status';
import { parseProblem, type ApiProblem } from './problems';

const API_BASE_PATH = '/api/v1';

export interface ApiRequestOptions {
	method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';
	body?: unknown;
	query?: Record<string, string | number | boolean | null | undefined>;
	signal?: AbortSignal;
}

export class ApiError extends Error {
	readonly problem: ApiProblem;

	constructor(problem: ApiProblem) {
		super(problem.detail ?? problem.title);
		this.name = 'ApiError';
		this.problem = problem;
	}
}

export function isApiProblem(error: unknown): error is ApiError {
	return error instanceof ApiError;
}

let unauthenticatedHandler: (() => void) | null = null;

export function setUnauthenticatedHandler(handler: (() => void) | null): void {
	unauthenticatedHandler = handler;
}

const UNSAFE_METHODS = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

function buildUrl(path: string, query: ApiRequestOptions['query']): URL {
	const url = new URL(`${API_BASE_PATH}${path}`, window.location.origin);
	if (query) {
		for (const [key, value] of Object.entries(query)) {
			if (value === undefined || value === null) continue;
			url.searchParams.set(key, String(value));
		}
	}
	return url;
}

export async function apiRequest<T>(
	path: string,
	options: ApiRequestOptions = {},
): Promise<T> {
	const method = options.method ?? 'GET';
	const hasBody = options.body !== undefined;
	const headers: Record<string, string> = {};
	if (hasBody) headers['Content-Type'] = 'application/json';

	const response = await fetch(buildUrl(path, options.query), {
		method,
		credentials: 'same-origin',
		headers,
		body: hasBody ? JSON.stringify(options.body) : undefined,
		signal: options.signal,
	});

	if (!response.ok) {
		const problem = await parseProblem(response);
		if (problem.code === 'unauthenticated') unauthenticatedHandler?.();
		if (problem.code === 'workspace_suspended') markWorkspaceSuspended();
		throw new ApiError(problem);
	}

	if (UNSAFE_METHODS.has(method)) clearWorkspaceSuspended();

	if (response.status === 204) return undefined as T;
	const text = await response.text();
	if (text.length === 0) return undefined as T;
	return JSON.parse(text) as T;
}
