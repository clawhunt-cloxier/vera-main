import { createFileRoute } from '@tanstack/react-router';
import { Watchlist } from '../pages/watchlist';

export const Route = createFileRoute('/app/watchlist/')({ component: Watchlist });
