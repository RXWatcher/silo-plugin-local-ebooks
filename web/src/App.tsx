import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import Libraries from "./pages/Libraries";
import Scans from "./pages/Scans";
import Metadata from "./pages/Metadata";
import Diagnostics from "./pages/Diagnostics";
import { ArrowLeft } from "lucide-react";

export default function App() {
  return (
    <div className="mx-auto max-w-6xl space-y-4 p-6">
      <div className="flex flex-wrap items-center gap-3">
        <a
          href="/admin/plugins"
          className="text-muted-foreground hover:bg-accent hover:text-accent-foreground inline-flex min-h-9 items-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition-colors"
          title="Back to Continuum plugins"
        >
          <ArrowLeft className="size-4" />
          <span>Continuum</span>
        </a>
        <span className="text-border" aria-hidden>/</span>
        <h1 className="text-2xl font-semibold">Local Ebooks — Operator Console</h1>
      </div>
      <Tabs defaultValue="libraries">
        <TabsList>
          <TabsTrigger value="libraries">Libraries</TabsTrigger>
          <TabsTrigger value="scans">Scans</TabsTrigger>
          <TabsTrigger value="metadata">Metadata</TabsTrigger>
          <TabsTrigger value="diagnostics">Diagnostics</TabsTrigger>
        </TabsList>
        <TabsContent value="libraries"><Libraries /></TabsContent>
        <TabsContent value="scans"><Scans /></TabsContent>
        <TabsContent value="metadata"><Metadata /></TabsContent>
        <TabsContent value="diagnostics"><Diagnostics /></TabsContent>
      </Tabs>
    </div>
  );
}
