import { Link, Outlet } from '@tanstack/react-router';
import { Card } from '@/components/ui/card';

export function PublicLayout() {
	return (
		<div className="flex min-h-screen flex-col items-center justify-center gap-6 bg-muted/30 p-4">
			<Link
				to="/"
				className="text-sm font-medium tracking-tight text-muted-foreground hover:text-foreground"
			>
				o1 uptime
			</Link>
			<Card className="w-full max-w-md">
				<Outlet />
			</Card>
		</div>
	);
}
