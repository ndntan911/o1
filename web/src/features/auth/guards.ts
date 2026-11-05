import { isApiProblem } from '@/lib/api/client';
import type { Me, Workspace } from '@/lib/api/types';

function hasControlCharacter(value: string): boolean {
	for (let index = 0; index < value.length; index += 1) {
		const code = value.charCodeAt(index);
		if (code <= 0x1f || code === 0x7f) return true;
	}
	return false;
}

export function safeRedirect(value: string | undefined): string {
	if (!value) return '/';
	if (!value.startsWith('/')) return '/';
	if (value.startsWith('//')) return '/';
	if (value.includes('\\')) return '/';
	if (hasControlCharacter(value)) return '/';
	return value;
}

export function findWorkspace(
	me: Me | null | undefined,
	slug: string,
): Workspace | undefined {
	return me?.workspaces.find((workspace) => workspace.slug === slug);
}

export function isUnauthenticated(error: unknown): boolean {
	return isApiProblem(error) && error.problem.code === 'unauthenticated';
}
