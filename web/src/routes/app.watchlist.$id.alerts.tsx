import { createFileRoute } from '@tanstack/react-router';
import { Alerts } from '../pages/watchlist';

export const Route = createFileRoute('/app/watchlist/$id/alerts')({ component: () => <Alerts id={Route.useParams().id} /> });
