export const LAST_WORKSPACE_KEY = 'o1.lastWorkspaceSlug';

export function readLastWorkspaceSlug(): string | null {
	try {
		return localStorage.getItem(LAST_WORKSPACE_KEY);
	} catch {
		return null;
	}
}

export function writeLastWorkspaceSlug(slug: string): void {
	try {
		localStorage.setItem(LAST_WORKSPACE_KEY, slug);
	} catch {
		// Storage may be unavailable (private mode, quota, or blocked cookies).
	}
}
