import { apiRequest } from '@/lib/api/client';
import type {
	PublicStatusPage,
	StatusPageConfig,
	StatusPageConfigInput,
} from '@/lib/api/types';

function statusPagePath(workspaceId: string): string {
	return `/workspaces/${workspaceId}/status-page`;
}

export function getStatusPage(workspaceId: string): Promise<StatusPageConfig> {
	return apiRequest<StatusPageConfig>(statusPagePath(workspaceId));
}

export function updateStatusPage(
	workspaceId: string,
	input: StatusPageConfigInput,
): Promise<StatusPageConfig> {
	return apiRequest<StatusPageConfig>(statusPagePath(workspaceId), {
		method: 'PUT',
		body: input,
	});
}

export function getPublicStatus(slug: string): Promise<PublicStatusPage> {
	return apiRequest<PublicStatusPage>(
		`/public/status/${encodeURIComponent(slug)}`,
	);
}
