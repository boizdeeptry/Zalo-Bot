import test from "node:test";
import assert from "node:assert/strict";

import { NAVIGATION } from "../overlay/internal/webui/static/core/router.js";

test("navigation exposes the legacy menu contract", () => {
  const publicContract = NAVIGATION.map(({ heading, items }) => ({
    heading,
    items: items.map(({ id, icon, label, todo, href }) => ({
      id,
      icon,
      label,
      todo,
      href,
    })),
  }));

  assert.deepEqual(publicContract, [
    {
      heading: "BUILD",
      items: [
        { id: "agents", icon: "🤖", label: "AI Agents", todo: undefined, href: undefined },
        { id: "knowledge", icon: "🧠", label: "Knowledge", todo: undefined, href: undefined },
        {
          id: "workflows",
          icon: "🔄",
          label: "Workflows",
          todo: "Chuỗi bước tự động: nhận tin thì làm gì, khi nào chuyển người thật, khi nào gửi tệp.",
          href: undefined,
        },
        {
          id: "tools",
          icon: "🛠",
          label: "Tools & MCP",
          todo: "Nối agent với công cụ ngoài qua MCP: CRM, đơn hàng, tồn kho.",
          href: undefined,
        },
        { id: "providers", icon: "🔌", label: "Providers", todo: undefined, href: undefined },
        { id: "models", icon: "🧩", label: "Models", todo: undefined, href: undefined },
        {
          id: "memory",
          icon: "🗃",
          label: "Memory",
          todo: "Ghi chú bot tự viết cho từng hội thoại, và bài học rút từ lần người trực sửa câu. Hiện xem trong trang Zalo.",
          href: undefined,
        },
      ],
    },
    {
      heading: "OPERATE",
      items: [
        { id: "convo", icon: "💬", label: "Conversations", todo: undefined, href: "/zalo" },
        {
          id: "analytics",
          icon: "📊",
          label: "Analytics",
          todo: "Số tin, số lượt bot trả lời, tỉ lệ phải chuyển người thật, cảm xúc khách để lại.",
          href: undefined,
        },
        {
          id: "eval",
          icon: "🧪",
          label: "Evaluation",
          todo: "Bộ câu hỏi mẫu chạy lại sau mỗi lần sửa văn phong, để biết sửa xong tốt hơn hay xấu đi.",
          href: undefined,
        },
        {
          id: "deploy",
          icon: "🚀",
          label: "Deployments",
          todo: "Chạy nhiều tài khoản Zalo, hoặc chuyển sang máy chủ.",
          href: undefined,
        },
      ],
    },
    {
      heading: "GOVERN",
      items: [
        {
          id: "security",
          icon: "🛡",
          label: "Security",
          todo: "Ai vào được portal, ai đọc được hội thoại, nhật ký truy cập.",
          href: undefined,
        },
        {
          id: "workspace",
          icon: "🏢",
          label: "Workspace",
          todo: "Nhiều người trực cùng dùng, phân quyền theo người.",
          href: undefined,
        },
      ],
    },
    {
      heading: "SYSTEM",
      items: [
        {
          id: "settings",
          icon: "⚙",
          label: "Settings",
          todo: "Cửa sổ gom tin, tên bot, thư mục tri thức. Hiện đặt trong trang Zalo và trong Chay.bat.",
          href: undefined,
        },
      ],
    },
  ]);
});
