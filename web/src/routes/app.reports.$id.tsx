import { createFileRoute } from '@tanstack/react-router';
import { ReportPage } from '../pages/reports';

export const Route = createFileRoute('/app/reports/$id')({ component: () => <ReportPage id={Route.useParams().id} /> });
