import { apiRequest } from '@/lib/api/client';
import type { APIKey, CreatedAPIKey, CreateAPIKeyInput } from '@/lib/api/types';

function apiKeysPath(workspaceId: string): string {
	return `/workspaces/${workspaceId}/api-keys`;
}

export function listAPIKeys(workspaceId: string): Promise<{ keys: APIKey[] }> {
	return apiRequest<{ keys: APIKey[] }>(apiKeysPath(workspaceId));
}

export function createAPIKey(
	workspaceId: string,
	input: CreateAPIKeyInput,
): Promise<CreatedAPIKey> {
	return apiRequest<CreatedAPIKey>(apiKeysPath(workspaceId), {
		method: 'POST',
		body: input,
	});
}

export function revokeAPIKey(
	workspaceId: string,
	keyId: string,
): Promise<void> {
	return apiRequest<void>(`${apiKeysPath(workspaceId)}/${keyId}`, {
		method: 'DELETE',
	});
}
