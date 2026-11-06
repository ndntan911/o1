import { describe, expect, test } from 'vitest';
import { ApiError } from './client';
import { problemFromError } from './problem-from-error';

describe('problemFromError', () => {
	test('returns the ApiError problem unchanged', () => {
		const problem = {
			type: 'about:blank',
			title: 'Forbidden',
			status: 403,
			code: 'forbidden',
		};

		expect(problemFromError(new ApiError(problem))).toBe(problem);
	});

	test('synthesizes an internal_error problem for network and unknown errors', () => {
		expect(problemFromError(new TypeError('Failed to fetch'))).toMatchObject({
			status: 0,
			code: 'internal_error',
			title: 'Something went wrong',
		});
		expect(problemFromError('unexpected')).toMatchObject({
			status: 0,
			code: 'internal_error',
		});
	});
});
