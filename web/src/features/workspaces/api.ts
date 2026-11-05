import { apiRequest } from '@/lib/api/client';
import type {
	Member,
	MemberListResponse,
	Role,
	Workspace,
} from '@/lib/api/types';

export function createWorkspace(name: string): Promise<Workspace> {
	return apiRequest<Workspace>('/workspaces', {
		method: 'POST',
		body: { name },
	});
}

export async function listMembers(workspaceId: string): Promise<Member[]> {
	const response = await apiRequest<MemberListResponse>(
		`/workspaces/${workspaceId}/members`,
	);
	return response.members;
}

export function addMember(
	workspaceId: string,
	input: { email: string; role: Role },
): Promise<void> {
	return apiRequest<void>(`/workspaces/${workspaceId}/members`, {
		method: 'POST',
		body: input,
	});
}
