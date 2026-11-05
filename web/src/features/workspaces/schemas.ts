import { z } from 'zod';

export const workspaceNameSchema = z
	.string()
	.trim()
	.min(1, 'Workspace name is required')
	.max(80, 'Workspace name must be at most 80 characters');

export const addMemberSchema = z.object({
	email: z.string().trim().toLowerCase().email('Enter a valid email address'),
	role: z.enum(['viewer', 'member', 'admin']),
});

export type AddMemberValues = z.infer<typeof addMemberSchema>;
