import { createRouter, createWebHashHistory } from "vue-router";

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {
      path: "/",
      component: () => import("@/layouts/AdminLayout.vue"),
      children: [
        { path: "", redirect: { name: "tools" } },
        {
          path: "tools",
          name: "tools",
          component: () => import("@/views/ToolListView.vue"),
        },
        {
          path: "tools/create",
          name: "tool-create",
          component: () => import("@/views/ToolCreateView.vue"),
        },
        {
          path: "tools/:id/edit",
          name: "tool-edit",
          component: () => import("@/views/ToolEditView.vue"),
        },
        {
          path: "permissions",
          name: "permissions",
          component: () => import("@/views/ToolPermissionsView.vue"),
        },
      ],
    },
  ],
});

export default router;
