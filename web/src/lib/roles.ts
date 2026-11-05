import type { Role } from '@/lib/api/types';

export const ROLE_RANK: Record<Role, number> = {
	viewer: 0,
	member: 1,
	admin: 2,
	owner: 3,
};

const ASSIGNABLE_ROLES: Role[] = ['viewer', 'member', 'admin'];

export function roleAtLeast(role: Role, minimum: Role): boolean {
	return ROLE_RANK[role] >= ROLE_RANK[minimum];
}

export function canManageMembers(role: Role): boolean {
	return roleAtLeast(role, 'admin');
}

export function canManageMonitors(role: Role): boolean {
	return roleAtLeast(role, 'member');
}

export function assignableRoles(role: Role): Role[] {
	if (!canManageMembers(role)) return [];
	return ASSIGNABLE_ROLES.filter(
		(candidate) => ROLE_RANK[candidate] < ROLE_RANK[role],
	);
}
