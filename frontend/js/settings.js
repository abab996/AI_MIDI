/* 设置页逻辑 */
(function () {
  "use strict";
  var UI = window.UI;
  var $ = UI.qs                                               ;

  var current      = null;
  var providers        = [];
  var presets        = [];
  var profiles        = [];
  var selectedProviderId = "";
  var activeProviderId = "";
  var activeModelId = "";
  var discovered        = [];
  var presetOpen = false;

  function newProviderId() {
    return "p_" + Math.random().toString(16).slice(2, 10);
  }

  function findProvider(id        ) {
    for (var i = 0; i < providers.length; i++) {
      if (providers[i].id === id) return providers[i];
    }
    return null;
  }

  function uniqueName(base        ) {
    var used      = {};
    providers.forEach(function (p) { used[p.name] = true; });
    if (!used[base]) return base;
    var n = 2;
    while (used[base + " " + n]) n++;
    return base + " " + n;
  }

  function matchProfile(id        ) {
    var raw = String(id || "").trim().toLowerCase();
    if (!raw) return null;
    var cands = [raw];
    var slash = raw.lastIndexOf("/");
    if (slash >= 0 && slash < raw.length - 1) cands.push(raw.slice(slash + 1));
    var best      = null;
    profiles.forEach(function (p) {
      var m = String(p.match || "").toLowerCase();
      cands.forEach(function (c) {
        if (!m || c.length < m.length || c.indexOf(m) !== 0) return;
        var boundary = c.length === m.length || m.charAt(m.length - 1) === "-" || "-.:/".indexOf(c.charAt(m.length)) >= 0;
        if (!boundary) return;
        if (!best || m.length > String(best.match).length) best = p;
      });
    });
    return best;
  }

  function applyProfile(model     ) {
    var hit = matchProfile(model.id);
    model._context = hit && hit.context_window ? hit.context_window : 0;
    if (!hit) return false;
    if (hit.max_tokens != null) model.max_tokens = hit.max_tokens;
    if (hit.max_completion_tokens != null) model.max_completion_tokens = hit.max_completion_tokens;
    if (hit.reasoning_effort) model.reasoning_effort = hit.reasoning_effort;
    if (hit.thinking_enabled != null) model.thinking_enabled = !!hit.thinking_enabled;
    return true;
  }

  function blankModel(id        ) {
    var model      = {
      id: id,
      max_tokens: null,
      max_completion_tokens: null,
      reasoning_effort: "",
      thinking_enabled: null,
    };
    applyProfile(model);
    return model;
  }

  function readDetail() {
    var p = findProvider(selectedProviderId);
    var form = $("#providerForm");
    if (!p || !form || form.hidden) return;
    p.name = $("#provName").value.trim() || p.name;
    p.base_url = $("#baseUrl").value.trim();
    p.api_path = $("#apiPath").value.trim();
    p.api_key = $("#apiKey").value;
    var proto = "openai";
    UI.qsa(".fn.active", $("#protocolSelector")).forEach(function (b) {
      proto = (b.dataset.protocol          ) || proto;
    });
    p.protocol = proto;
  }

  function setProtocol(protocol        ) {
    UI.qsa(".fn", $("#protocolSelector")).forEach(function (b) {
      b.classList.toggle("active", b.dataset.protocol === protocol);
    });
    updateProtocolHint(protocol);
  }

  /* 接入点只显示主机名，完整地址在 title 里 —— 侧栏一行放得下 */
  function hostOf(url        ) {
    var s = String(url || "").trim();
    if (!s) return "未填地址";
    var m = /^[a-z][a-z0-9+.-]*:\/\/([^/?#]+)/i.exec(s);
    return (m ? m[1] : s).replace(/\/+$/, "");
  }

  function protoTag(protocol        ) {
    return protocol === "anthropic" ? "ANT" : (protocol === "gemini" ? "GEM" : "OAI");
  }

  var PROTO_HINT      = {
    openai: "OpenAI 兼容协议：Bearer 鉴权，请求发到 /chat/completions。绝大多数服务商选这个。",
    anthropic: "Anthropic 原生协议：x-api-key 鉴权 + anthropic-version 头，请求发到 /messages。",
    gemini: "Google 的 OpenAI 兼容层（预设路径 /v1beta/openai）。原生 generateContent 不适用。",
  };

  function renderProviderList() {
    var list = $("#providerList");
    var count = $("#providerCount");
    if (count) count.textContent = String(providers.length);
    if (!list) return;
    list.innerHTML = "";
    if (!providers.length) {
      var empty = document.createElement("div");
      empty.className = "rail-empty";
      empty.textContent = "还没有供应商";
      list.appendChild(empty);
      return;
    }
    providers.forEach(function (p) {
      var row = document.createElement("div");
      row.className = "provider-item" + (p.id === selectedProviderId ? " active" : "") +
        (p.enabled ? "" : " off") + (p.id === activeProviderId ? " in-use" : "");
      row.title = (p.name || "未命名") + "\n" + (p.base_url || "") + (p.api_path || "") +
        "\n协议：" + protoTag(p.protocol);

      /* 品牌图标顶掉了原来的 OAI/ANT/GEM 协议章——协议在详情页头和这一行的
         tooltip 里都看得到，图标位留给更好认的品牌标识 */
      row.appendChild(BrandIcons.forPreset(p.preset_id || "", p.name));

      var text = document.createElement("span");
      text.className = "prov-text";
      var name = document.createElement("span");
      name.className = "prov-name";
      name.textContent = p.name || "未命名";
      var host = document.createElement("span");
      host.className = "prov-host";
      host.textContent = hostOf(p.base_url);
      text.appendChild(name);
      text.appendChild(host);

      var sw = document.createElement("button");
      sw.type = "button";
      sw.className = "provider-switch" + (p.enabled ? " on" : "");
      sw.title = p.enabled ? "已启用：出现在对话的模型菜单里（点击停用）" : "已停用（点击启用）";
      sw.setAttribute("aria-label", sw.title);
      sw.addEventListener("click", function (e) {
        e.stopPropagation();
        readDetail();
        p.enabled = !p.enabled;
        if (!p.enabled && activeProviderId === p.id) {
          activeProviderId = "";
          activeModelId = "";
        }
        renderProviderList();
        fillDetail();
        persistProviders();
      });

      row.appendChild(text);
      row.appendChild(sw);
      row.addEventListener("click", function () {
        readDetail();
        selectedProviderId = p.id;
        discovered = [];
        renderProviderList();
        fillDetail();
      });
      list.appendChild(row);
    });
  }

  /* 详情页头：名称 + 协议章 + 主机名，随输入实时更新 */
  function renderHead(p     ) {
    var name = $("#provHeadName");
    var proto = $("#provHeadProto");
    var host = $("#provHeadHost");
    var stamp = $("#provActiveStamp");
    if (name) name.textContent = (p && p.name) || "未命名";
    if (proto) proto.textContent = protoTag((p && p.protocol) || "openai");
    if (host) {
      var h = hostOf(p && p.base_url);
      host.textContent = h;
      host.title = ((p && p.base_url) || "") + ((p && p.api_path) || "");
    }
    if (stamp) stamp.hidden = !(p && p.id === activeProviderId);
  }

  /* 展开状态按「供应商/模型」记住：重渲染（改参数、加模型）不该把卡片合上 */
  var openModels      = {};

  var EFFORT_ORDER = ["low", "medium", "max"];

  /* 折叠时那行摘要：只列"填过的"项，没填的不写——以前把三项都写成"沿用"，
     用户根本看不出"沿用"是啥意思。没填的到底怎么算，卡片展开后有逐项说明。 */
  function specText(m     ) {
    var bits = [];
    if (m._context) bits.push("上下文 " + Number(m._context).toLocaleString("en-US"));
    if (m.max_tokens != null) bits.push("max_tokens " + m.max_tokens);
    if (m.max_completion_tokens != null) bits.push("最大输出 " + m.max_completion_tokens);
    if (!modelThinkingOn(m)) {
      bits.push("不思考");
      return bits.join(" · ");
    }
    var on = enabledEfforts(m);
    if (on.length) {
      var picked = (m.reasoning_effort || "").toLowerCase();
      var eff = on.indexOf(picked) >= 0 ? picked : on[0];
      bits.push("思考强度 " + eff.toUpperCase() + "（可选 " + on.map(function (l) { return l.toUpperCase(); }).join("/") + "）");
    } else {
      bits.push("不调节思考强度");
    }
    return bits.join(" · ");
  }

  function renderModelCards() {
    var host = $("#modelCards");
    var p = findProvider(selectedProviderId);
    var count = $("#modelCount");
    if (count) count.textContent = p ? String((p.models || []).length) : "0";
    if (!host) return;
    host.innerHTML = "";
    if (!p) return;
    if (!(p.models || []).length) {
      var empty = document.createElement("div");
      empty.className = "model-empty";
      empty.textContent = "这个供应商还没有可用模型——刷新列表勾选，或在下面手填模型 ID。";
      host.appendChild(empty);
      return;
    }
    (p.models || []).forEach(function (m     ) {
      var key = p.id + "/" + m.id;
      /* 用 button + 可动画的高度容器代替 <details>：<details> 的开合没法做
         过渡（内容在关闭时根本不参与布局），而这里要跟全站的展开动画一致 */
      var card = document.createElement("div");
      card.className = "model-card" + (activeProviderId === p.id && activeModelId === m.id ? " in-use" : "");
      var isOpen = !!openModels[key];
      if (isOpen) card.classList.add("open");

      var head = document.createElement("button");
      head.type = "button";
      head.className = "model-card-head";
      head.setAttribute("aria-expanded", isOpen ? "true" : "false");
      var caret = document.createElement("span");
      caret.className = "model-caret";
      caret.textContent = "▸";
      var title = document.createElement("span");
      title.className = "model-id";
      title.textContent = m.id;
      title.title = m.id;

      var del = document.createElement("button");
      del.type = "button";
      del.className = "btn btn-danger btn-sm model-del";
      del.textContent = "移除";
      del.title = "从可用模型里移除";
      del.addEventListener("click", function (e) {
        /* 表头本身是开合按钮，行内按钮的点击不能连带把卡片收起来 */
        e.preventDefault();
        e.stopPropagation();
        p.models = p.models.filter(function (x     ) { return x !== m; });
        if (activeProviderId === p.id && activeModelId === m.id) activeModelId = "";
        delete openModels[key];
        renderModelCards();
        persistProviders();
      });

      var meta = document.createElement("span");
      meta.className = "model-spec";
      meta.textContent = specText(m);

      var rowTop = document.createElement("span");
      rowTop.className = "model-row-top";
      rowTop.appendChild(caret);
      rowTop.appendChild(BrandIcons.forModel(m.id, p.preset_id || "", m.id));
      rowTop.appendChild(title);
      if (activeProviderId === p.id && activeModelId === m.id) {
        var inUse = document.createElement("span");
        inUse.className = "stamp ok model-in-use";
        inUse.textContent = "● 使用中";
        rowTop.appendChild(inUse);
      }
      rowTop.appendChild(del);

      var rowMeta = document.createElement("span");
      rowMeta.className = "model-row-meta";
      rowMeta.appendChild(meta);

      head.appendChild(rowTop);
      head.appendChild(rowMeta);
      head.addEventListener("click", function () {
        isOpen = !isOpen;
        if (isOpen) openModels[key] = true;
        else delete openModels[key];
        card.classList.toggle("open", isOpen);
        head.setAttribute("aria-expanded", isOpen ? "true" : "false");
      });
      card.appendChild(head);

      /* 收起时高度为 0（grid-template-rows: 0fr），展开过渡到 1fr —— 不需要
         测高度，内容多高都能动画 */
      var bodyWrap = document.createElement("div");
      bodyWrap.className = "model-card-body-wrap";
      var body = document.createElement("div");
      body.className = "model-card-body";

      var grid = document.createElement("div");
      grid.className = "field-grid";
      grid.appendChild(numField("最大上下文 · max_tokens", m.max_tokens, function (v) {
        m.max_tokens = v;
        meta.textContent = specText(m);
      }));
      grid.appendChild(numField("最大输出 · max_completion_tokens", m.max_completion_tokens, function (v) {
        m.max_completion_tokens = v;
        meta.textContent = specText(m);
      }));
      body.appendChild(grid);

      body.appendChild(effortToggles(m, meta));

      var think = document.createElement("label");
      think.className = "toggle-text model-think";
      var cb = document.createElement("input");
      cb.type = "checkbox";
      cb.checked = m.thinking_enabled == null ? false : !!m.thinking_enabled;
      var span = document.createElement("span");
      span.textContent = "启用 thinking";
      var follow = document.createElement("span");
      follow.className = "dim model-follow";
      follow.textContent = m.thinking_enabled == null ? "用「生成参数」页的默认值" : "该模型单独指定";
      cb.addEventListener("change", function () {
        m.thinking_enabled = cb.checked;
        follow.textContent = "该模型单独指定";
        meta.textContent = specText(m);
        /* 不思考 ↔ 思考切换会改变对话页菜单的可见性，卡片提示跟着说清楚；
           与档位开关一样即时落库——否则这页改了、对话页的菜单却还是老样子 */
        renderModelCards();
        persistProviders();
      });
      think.appendChild(cb);
      think.appendChild(span);
      think.appendChild(follow);
      body.appendChild(think);

      if (m._context) {
        var hint = document.createElement("div");
        hint.className = "stat-line";
        var dim = document.createElement("span");
        dim.className = "dim";
        dim.textContent = "识别到的上下文窗口：" + Number(m._context).toLocaleString("en-US") + "（仅界面提示，不单独发往 API）";
        hint.appendChild(dim);
        body.appendChild(hint);
      } else if (!matchProfile(m.id)) {
        var miss = document.createElement("div");
        miss.className = "stat-line";
        var dim2 = document.createElement("span");
        dim2.className = "dim";
        dim2.textContent = "目录里没有这个模型：没填的项都用「生成参数」页的默认值。";
        miss.appendChild(dim2);
        body.appendChild(miss);
      }

      bodyWrap.appendChild(body);
      card.appendChild(bodyWrap);
      host.appendChild(card);
    });
  }

  /* 模型卡里的思考强度：逐档开关。点亮 = 这个模型支持这一档，只有点亮的档位
     才会出现在对话页的思考强度菜单里；全部不点亮 = 该模型不调节思考强度。 */
  function effortToggles(m     , meta             ) {
    var wrap = document.createElement("div");
    wrap.className = "model-effort";

    var lab = document.createElement("div");
    lab.className = "field-label";
    lab.textContent = "支持的思考强度 · reasoning_effort";
    wrap.appendChild(lab);

    var chips = document.createElement("div");
    chips.className = "effort-chips";
    wrap.appendChild(chips);

    var hint = document.createElement("div");
    hint.className = "model-effort-hint";
    wrap.appendChild(hint);

    function paint() {
      var on = enabledEfforts(m);
      chips.innerHTML = "";
      EFFORT_ORDER.forEach(function (level) {
        var b = document.createElement("button");
        b.type = "button";
        b.className = "effort-chip" + (on.indexOf(level) >= 0 ? " on" : "");
        b.textContent = level.toUpperCase();
        b.setAttribute("aria-pressed", on.indexOf(level) >= 0 ? "true" : "false");
        b.title = on.indexOf(level) >= 0 ? "已启用（点击关闭）" : "未启用（点击开启）";
        b.addEventListener("click", function () {
          var next = on.slice();
          var at = next.indexOf(level);
          if (at >= 0) next.splice(at, 1);
          else next.push(level);
          /* 与后端 normalizeModels 同一套规则：按规范顺序排好，选中档位
             如果被关掉了就顺延到还开着的第一档，一档不剩就清空 */
          m.reasoning_efforts = EFFORT_ORDER.filter(function (l) { return next.indexOf(l) >= 0; });
          if (!m.reasoning_efforts.length) m.reasoning_effort = "";
          else if (m.reasoning_efforts.indexOf(m.reasoning_effort) < 0) {
            m.reasoning_effort = m.reasoning_efforts[0];
          }
          paint();
          meta.textContent = specText(m);
          persistProviders();
        });
        chips.appendChild(b);
      });
      if (!modelThinkingOn(m)) {
        hint.textContent = "该模型已设为不思考：对话页不会显示思考强度菜单。想恢复，勾选下方的「启用 thinking」。";
        return;
      }
      if (!on.length) {
        hint.textContent = "一档都没开：这个模型不会收到思考强度参数。";
        return;
      }
      var picked = (m.reasoning_effort || "").toLowerCase();
      hint.textContent = "点亮的档位才能在对话页选；当前对话用它的是 " +
        (on.indexOf(picked) >= 0 ? picked.toUpperCase() : on[0].toUpperCase()) + "。";
    }

    paint();
    return wrap;
  }

  /* 该模型启用了哪些档位。后端字段为 nil 表示"还没配过"，这时按模型目录的
     声明当作启用集（旧数据平滑过渡，保存一次后就固化成显式配置）。 */
  function enabledEfforts(m     ) {
    if (m.reasoning_efforts == null) return supportedEfforts(m.id);
    return m.reasoning_efforts            ;
  }

  /* 这个模型实际上会不会思考：模型单独指定的优先，没指定就看「生成参数」页
     的全局开关。thinking 关着的模型在对话页不显示档位菜单。 */
  function modelThinkingOn(m     ) {
    if (m.thinking_enabled === true) return true;
    if (m.thinking_enabled === false) return false;
    var g = $("#thinkingEnabled")                           ;
    return !g || g.checked;
  }

  /* 模型目录里的档位声明；目录条目没写就是"不单独调节"，目录外按全档位。
     与后端 llm.SupportedReasoningEfforts 同一套规则，只是在前端本地算。 */
  function supportedEfforts(id        ) {
    var hit = matchProfile(id);
    if (!hit) return ["low", "medium", "max"];
    return (hit.reasoning_efforts            ) || [];
  }

  function numField(label        , value     , onChange                  ) {
    var wrap = document.createElement("div");
    wrap.className = "field";
    var lab = document.createElement("label");
    lab.className = "field-label";
    lab.textContent = label;
    var input = document.createElement("input");
    input.type = "number";
    input.placeholder = "留空 = 用「生成参数」页的默认值";
    input.value = value == null ? "" : String(value);
    input.addEventListener("change", function () {
      var v = input.value;
      if (v === "") onChange(null);
      else {
        var n = Number(v);
        onChange(n === 0 ? null : n);
      }
    });
    wrap.appendChild(lab);
    wrap.appendChild(input);
    return wrap;
  }

  function renderDiscovered() {
    var host = $("#discoveredList");
    var p = findProvider(selectedProviderId);
    var addBtn      = $("#addDiscoveredBtn");
    if (!host) return;
    host.innerHTML = "";
    var ids = discovered;
    if (!ids.length || !p) {
      host.hidden = true;
      if (addBtn) addBtn.disabled = true;
      return;
    }
    host.hidden = false;
    var have      = {};
    (p.models || []).forEach(function (m     ) { have[m.id] = true; });
    ids.forEach(function (id) {
      var lab = document.createElement("label");
      lab.className = "discovered-item" + (have[id] ? " added" : "");
      var cb = document.createElement("input");
      cb.type = "checkbox";
      cb.disabled = !!have[id];
      cb.checked = !!have[id];
      cb.dataset.mid = id;
      cb.addEventListener("change", syncAddBtn);
      var text = document.createElement("span");
      text.className = "discovered-id";
      text.textContent = id;
      text.title = id;
      lab.appendChild(cb);
      lab.appendChild(text);
      if (have[id]) {
        var tag = document.createElement("span");
        tag.className = "discovered-tag";
        tag.textContent = "已添加";
        lab.appendChild(tag);
      }
      host.appendChild(lab);
    });
    syncAddBtn();
  }

  /* 没有任何勾选时「添加勾选」是空操作，直接置灰更诚实 */
  function syncAddBtn() {
    var addBtn      = $("#addDiscoveredBtn");
    if (!addBtn) return;
    var host = $("#discoveredList");
    var n = 0;
    UI.qsa("input[type=checkbox]", host).forEach(function (el) {
      var box = el                    ;
      if (box.checked && !box.disabled) n++;
    });
    addBtn.disabled = n === 0;
  }

  function updateProtocolHint(protocol        ) {
    var el = $("#protocolHint");
    if (el) el.textContent = PROTO_HINT[protocol] || PROTO_HINT.openai;
  }

  function fillDetail() {
    var p = findProvider(selectedProviderId);
    var empty = $("#providerEmpty");
    var form = $("#providerForm");
    if (!p) {
      if (empty) empty.hidden = false;
      if (form) form.hidden = true;
      return;
    }
    if (empty) empty.hidden = true;
    if (form) form.hidden = false;
    $("#provName").value = p.name || "";
    $("#apiKey").value = p.api_key || "";
    $("#baseUrl").value = p.base_url || "";
    $("#apiPath").value = p.api_path || "";
    setProtocol(p.protocol || "openai");
    var keyInput      = $("#apiKey");
    if (keyInput) {
      keyInput.type = "password";
      /* 本地推理服务本来就不校验密钥，别让「预设里不含密钥」误导用户以为没配好 */
      keyInput.placeholder = UI.isLocalEndpoint && UI.isLocalEndpoint(p.base_url)
        ? "本地服务，可留空"
        : "由你填写，预设里不含密钥";
    }
    var showBtn = $("#showKeyBtn");
    if (showBtn) showBtn.textContent = "显示";
    renderHead(p);
    renderDiscovered();
    renderModelCards();
  }

  function closePresetMenu() {
    var menu = $("#presetMenu");
    if (!menu || !presetOpen) return;
    menu.hidden = true;
    menu.classList.remove("menu-in");
    presetOpen = false;
  }

  function openPresetMenu() {
    var menu = $("#presetMenu");
    if (!menu) return;
    menu.hidden = false;
    menu.innerHTML = "";
    var filter = document.createElement("input");
    filter.className = "preset-filter";
    filter.placeholder = "筛选名称 / 分组 / 地址";
    menu.appendChild(filter);
    var box = document.createElement("div");
    box.className = "preset-list";
    menu.appendChild(box);
    var foot = document.createElement("div");
    foot.className = "preset-foot";
    menu.appendChild(foot);
    function paint() {
      var q = filter.value.trim().toLowerCase();
      box.innerHTML = "";
      var group = "";
      var shown = 0;
      presets.forEach(function (pre) {
        var blob = (pre.name + " " + pre.group + " " + pre.base_url).toLowerCase();
        if (q && blob.indexOf(q) < 0) return;
        if (pre.group !== group) {
          group = pre.group;
          var h = document.createElement("div");
          h.className = "preset-group";
          h.textContent = group || "其他";
          box.appendChild(h);
        }
        shown++;
        var opt = document.createElement("div");
        opt.className = "select-option preset-option";
        opt.appendChild(BrandIcons.forPreset(pre.id, pre.name));
        var nm = document.createElement("span");
        nm.className = "preset-name";
        nm.textContent = pre.name;
        opt.appendChild(nm);
        /* OpenAI 兼容是默认预期，只有非默认协议才额外标出来 */
        if (protoTag(pre.protocol) !== "OAI") {
          var tag = document.createElement("span");
          tag.className = "prov-badge";
          tag.textContent = protoTag(pre.protocol);
          opt.appendChild(tag);
        }
        var url = document.createElement("span");
        url.className = "preset-url";
        url.textContent = hostOf(pre.base_url) + (pre.api_path || "");
        opt.appendChild(url);
        opt.title = pre.name + "\n" + pre.base_url + (pre.api_path || "") + "\n协议：" + protoTag(pre.protocol);
        opt.addEventListener("click", function () {
          addFromPreset(pre);
          closePresetMenu();
        });
        box.appendChild(opt);
      });
      if (!shown) {
        var none = document.createElement("div");
        none.className = "select-empty";
        none.textContent = "没有匹配的预设";
        box.appendChild(none);
      }
      foot.textContent = shown + "/" + presets.length + " 个预设 · 不含密钥";
    }
    filter.addEventListener("input", paint);
    filter.addEventListener("keydown", function (e) {
      if (e.key === "Enter") {
        var first = box.querySelector(".preset-option")                      ;
        if (first) first.click();
      }
    });
    paint();
    menu.classList.remove("menu-in");
    void menu.offsetWidth;
    menu.classList.add("menu-in");
    presetOpen = true;
    filter.focus();
  }

  function addFromPreset(pre     ) {
    readDetail();
    var p = {
      id: newProviderId(),
      preset_id: pre.id,
      name: uniqueName(pre.name),
      protocol: pre.protocol || "openai",
      base_url: pre.base_url,
      api_path: pre.api_path || "",
      api_key: "",
      enabled: true,
      models: [],
    };
    providers.push(p);
    selectedProviderId = p.id;
    discovered = [];
    renderProviderList();
    fillDetail();
    persistProviders();
  }

  function addCustom() {
    readDetail();
    var p = {
      id: newProviderId(),
      preset_id: "",
      name: uniqueName("自定义供应商"),
      protocol: "openai",
      base_url: "https://api.openai.com",
      api_path: "/v1",
      api_key: "",
      enabled: true,
      models: [],
    };
    providers.push(p);
    selectedProviderId = p.id;
    discovered = [];
    renderProviderList();
    fillDetail();
    persistProviders();
  }

  function publicProviders() {
    return providers.map(function (p) {
      return {
        id: p.id,
        preset_id: p.preset_id || "",
        name: p.name,
        protocol: p.protocol || "openai",
        base_url: p.base_url || "",
        api_path: p.api_path || "",
        api_key: p.api_key || "",
        enabled: !!p.enabled,
        models: (p.models || []).map(function (m     ) {
          return {
            id: m.id,
            max_tokens: m.max_tokens == null || m.max_tokens === "" ? null : Number(m.max_tokens),
            max_completion_tokens: m.max_completion_tokens == null || m.max_completion_tokens === "" ? null : Number(m.max_completion_tokens),
            reasoning_effort: m.reasoning_effort || "",
            /* 启用的档位集合：没配过保持 null（后端按"未配置"处理，按目录声明
               兜底），配过就送数组（可能是空数组 = 用户全关了） */
            reasoning_efforts: m.reasoning_efforts == null ? null : (m.reasoning_efforts            ).slice(),
            thinking_enabled: m.thinking_enabled == null ? null : !!m.thinking_enabled,
          };
        }),
      };
    });
  }

  function fillForm(s     ) {
    current = s;
    providers = JSON.parse(JSON.stringify(s.providers || []));
    providers.forEach(function (p) {
      (p.models || []).forEach(function (m     ) {
        applyProfileContextOnly(m);
        /* 旧数据没有"启用了哪些档位"这个字段：按模型目录的声明播种，界面上
           就是"目录认为支持的那几档默认开着"。保存一次后固化成显式配置。 */
        if (m.reasoning_efforts == null) m.reasoning_efforts = supportedEfforts(m.id).slice();
      });
    });
    activeProviderId = s.active_provider_id || "";
    activeModelId = s.active_model_id || "";
    if (!findProvider(selectedProviderId)) {
      selectedProviderId = activeProviderId || (providers[0] && providers[0].id) || "";
    }
    /* 全局推理强度要先落到 DOM 上再画详情：模型卡的摘要行会引用它，
       顺序反了会先按默认值画一遍 */
    var effort = (s.reasoning_effort || "max").toLowerCase();
    UI.qsa(".fn", $("#effortSelector")).forEach(function (b) {
      b.classList.toggle("active", b.dataset.effort === effort);
    });
    renderProviderList();
    fillDetail();
    $("#maxTokens").value = s.max_tokens == null ? "" : s.max_tokens;
    $("#maxCompletion").value = s.max_completion_tokens == null ? "" : s.max_completion_tokens;
    $("#thinkingEnabled").checked = !!s.thinking_enabled;
  }

  function applyProfileContextOnly(m     ) {
    var hit = matchProfile(m.id);
    m._context = hit && hit.context_window ? hit.context_window : 0;
  }

  function collect() {
    readDetail();
    var effort = "max";
    UI.qsa(".fn.active", $("#effortSelector")).forEach(function (b) {
      effort = b.dataset.effort       ;
    });
    function numOrNull(input     ) {
      if (!input) return null;
      var v = input.value;
      if (v === "") return null;
      var n = Number(v);
      return n === 0 ? null : n;
    }
    var active = findProvider(activeProviderId);
    return {
      api_key: active ? active.api_key : "",
      base_url: active ? active.base_url : ($("#baseUrl") ? $("#baseUrl").value : ""),
      api_path: active ? active.api_path : "",
      model: activeModelId,
      providers: publicProviders(),
      active_provider_id: activeProviderId,
      active_model_id: activeModelId,
      max_tokens: numOrNull($("#maxTokens")),
      max_completion_tokens: numOrNull($("#maxCompletion")),
      reasoning_effort: effort,
      thinking_enabled: $("#thinkingEnabled").checked,
    };
  }

  function persistProviders() {
    return UI.putJSON("/api/settings", collect()).then(function (data     ) {
      var st = $("#saveStatus");
      if (st) {
        st.textContent = data.message || "✓ 配置已保存";
        st.style.color = "var(--color-primary)";
      }
      return data;
    }).catch(function (e) {
      UI.toast ("✗ " + e.message, "err");
      throw e;
    });
  }

  /* ═══════════ MIDI 硬件设备管理 ═══════════ */
  var midiAccess      = null;
  var activeMidiInput      = null;
  var midiSignalTimer      = null;

  var MIDI_STORAGE_KEY = "ai-midi-hardware-settings";
  var DEFAULT_MIDI_SETTINGS = {
    enabled: true,
    deviceId: "",
    channel: "all",
    velocityCurve: "linear",
    typingKeyboard: true,
  };

  function loadMidiSettings() {
    try {
      var raw = localStorage.getItem(MIDI_STORAGE_KEY);
      return raw ? Object.assign({}, DEFAULT_MIDI_SETTINGS, JSON.parse(raw)) : DEFAULT_MIDI_SETTINGS;
    } catch (e) {
      return DEFAULT_MIDI_SETTINGS;
    }
  }

  function saveMidiSettings(s     ) {
    try {
      localStorage.setItem(MIDI_STORAGE_KEY, JSON.stringify(s));
    } catch (e) {}
  }

  function flashMidiSignal(note     , vel     ) {
    var stamp = $("#midiSignalStamp");
    if (!stamp) return;
    stamp.style.display = "inline-flex";
    stamp.className = "stamp ok";
    stamp.textContent = "● MIDI: " + note + " (v" + vel + ")";
    clearTimeout(midiSignalTimer);
    midiSignalTimer = setTimeout(function () {
      stamp.className = "stamp";
      stamp.textContent = "● MIDI IN";
    }, 400);
  }

  function onMidiMessage(e     ) {
    var data = e.data;
    if (!data || data.length < 2) return;
    var status = data[0] & 0xf0;
    var channel = (data[0] & 0x0f) + 1;
    var cfgChannel = $("#midiChannel") ? $("#midiChannel").value : "all";
    if (cfgChannel !== "all" && parseInt(cfgChannel, 10) !== channel) return;

    var note = data[1];
    var vel = data.length > 2 ? data[2] : 0;
    if (status === 0x90 && vel > 0) {
      flashMidiSignal(note, vel);
    }
  }

  var lastMidiDeviceSig = "";

  function scanMidiDevices(preferredId     , isBackgroundEvent      ) {
    var sel      = $("#midiDeviceSelect");
    var status = $("#midiDeviceStatus");
    if (!sel || !status) return;

    if (!navigator.requestMIDIAccess) {
      status.textContent = "当前环境/浏览器不支持 Web MIDI API";
      status.className = "dim err";
      sel.innerHTML = '<option value="">（不支持 Web MIDI API）</option>';
      return;
    }

    if (!isBackgroundEvent && !midiAccess) {
      status.textContent = "正在扫描 MIDI 设备…";
    }

    var promise = midiAccess ? Promise.resolve(midiAccess) : navigator.requestMIDIAccess();
    promise.then(function (access) {
      midiAccess = access;

      var inputs = Array.from(access.inputs.values());
      var currentSig = inputs.map(function (inp     ) { return inp.id + ":" + inp.state; }).join(",");

      // 仅在设备列表有真实物理变动时才重绘下拉列表与提示，彻底根治递归死循环抽搐
      if (currentSig !== lastMidiDeviceSig || !sel.options.length) {
        lastMidiDeviceSig = currentSig;
        sel.innerHTML = "";

        var optAuto = document.createElement("option");
        optAuto.value = "auto";
        optAuto.textContent = "自动连接首个可用设备 (Auto)";
        sel.appendChild(optAuto);

        if (!inputs.length) {
          var optNone = document.createElement("option");
          optNone.value = "";
          optNone.textContent = "（未检测到硬件 MIDI 键盘，仍可使用电脑键盘弹奏）";
          sel.appendChild(optNone);
          status.textContent = "未检测到外部 MIDI 输入设备";
          status.className = "dim";
        } else {
          inputs.forEach(function (inp     ) {
            var opt = document.createElement("option");
            opt.value = inp.id;
            opt.textContent = (inp.name || "MIDI 设备") + (inp.manufacturer ? " (" + inp.manufacturer + ")" : "");
            sel.appendChild(opt);
          });
          status.textContent = "✓ 已检测到 " + inputs.length + " 个 MIDI 输入设备";
          status.className = "dim ok";
        }

        var targetId = preferredId !== undefined ? preferredId : (sel.value || "auto");
        if (targetId && Array.from(sel.options).some(function (o     ) { return o.value === targetId; })) {
          sel.value = targetId;
        }

        bindSelectedMidiInput(sel.value);
      }

      // 仅绑定一次 onstatechange 事件
      if (!access._stateChangeBound) {
        access._stateChangeBound = true;
        access.onstatechange = function () {
          scanMidiDevices(sel.value, true);
        };
      }
    }).catch(function (err) {
      status.textContent = "获取 Web MIDI 权限失败: " + err.message;
      status.className = "dim err";
    });
  }

  function bindSelectedMidiInput(deviceId     ) {
    if (!midiAccess || !$("#midiInputEnabled") || !$("#midiInputEnabled").checked) {
      if (activeMidiInput) {
        try { activeMidiInput.onmidimessage = null; } catch (e) {}
        activeMidiInput = null;
      }
      var stamp = $("#midiSignalStamp");
      if (stamp) stamp.style.display = "none";
      return;
    }

    var inputs = Array.from(midiAccess.inputs.values());
    var target = null;
    if (deviceId === "auto" || !deviceId) {
      target = inputs[0] || null;
    } else {
      target = midiAccess.inputs.get(deviceId) || null;
    }

    // 若当前设备已正确挂载监听器，避免重复解绑与重新赋值触发端口状态事件
    if (activeMidiInput === target && target && target.onmidimessage === onMidiMessage) {
      var stamp = $("#midiSignalStamp");
      if (stamp) stamp.style.display = "inline-flex";
      return;
    }

    if (activeMidiInput) {
      try { activeMidiInput.onmidimessage = null; } catch (e) {}
      activeMidiInput = null;
    }

    if (target) {
      activeMidiInput = target;
      activeMidiInput.onmidimessage = onMidiMessage;
      var stamp = $("#midiSignalStamp");
      if (stamp) stamp.style.display = "inline-flex";
    }
  }

  function fillMidiSettings() {
    var ms = loadMidiSettings();
    if ($("#midiInputEnabled")) $("#midiInputEnabled").checked = ms.enabled !== false;
    if ($("#midiChannel")) $("#midiChannel").value = ms.channel || "all";
    if ($("#midiVelocityCurve")) $("#midiVelocityCurve").value = ms.velocityCurve || "linear";
    if ($("#typingKeyboardEnabled")) $("#typingKeyboardEnabled").checked = ms.typingKeyboard !== false;
    scanMidiDevices(ms.deviceId);
  }

  function collectMidiSettings() {
    return {
      enabled: $("#midiInputEnabled") ? $("#midiInputEnabled").checked : true,
      deviceId: $("#midiDeviceSelect") ? $("#midiDeviceSelect").value : "auto",
      channel: $("#midiChannel") ? $("#midiChannel").value : "all",
      velocityCurve: $("#midiVelocityCurve") ? $("#midiVelocityCurve").value : "linear",
      typingKeyboard: $("#typingKeyboardEnabled") ? $("#typingKeyboardEnabled").checked : true,
    };
  }

  /* ═══════════ 标签页（左侧标签 + 右侧面板） ═══════════ */

  var reducedMotion = typeof window.matchMedia === "function"
    && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  var activePane = "";
  var paneToken = 0;

  function clearPaneAnim(p     ) {
    p.classList.remove("sc-out-up", "sc-out-down", "sc-in-up", "sc-in-down");
  }

  /* 标签序号：新标签在旧标签之后 = 向下滚动，反之向上 */
  function tabOrderIndex(paneId     ) {
    var btns = UI.qsa(".settings-tab", $(".settings-tabs"));
    for (var i = 0; i < btns.length; i++) {
      if (btns[i].dataset.pane === paneId) return i;
    }
    return -1;
  }

  /* 初始化/复位：无动画直接同步标签与面板显隐 */
  function activatePane(paneId     ) {
    UI.qsa(".settings-tab", $(".settings-tabs")).forEach(function (t) {
      var on = t.dataset.pane === paneId;
      t.classList.toggle("active", on);
      t.setAttribute("aria-selected", on ? "true" : "false");
    });
    UI.qsa(".settings-pane", $(".settings-main")).forEach(function (p) {
      p.hidden = p.id !== paneId;
    });
    activePane = paneId;
  }

  /* 切换面板：纵向滚动 + 运动模糊——旧面板朝反方向带模糊滑出（0.34s），
     新面板从另一侧带模糊推入、落定时清晰（0.46s）；token 防快速连点竞态；
     reduced-motion 直接切换显隐 */
  function switchPane(btn     , paneId     ) {
    if (paneId === activePane) return;
    if (paneId === "paneShortcuts") renderShortcutsPane();
    if (paneId === "paneLibrary") loadLibraryFiles();
    paneToken++;
    var token = paneToken;

    UI.qsa(".settings-tab", $(".settings-tabs")).forEach(function (t) {
      var on = t === btn;
      t.classList.toggle("active", on);
      t.setAttribute("aria-selected", on ? "true" : "false");
    });
    btn.classList.add("pressed");
    setTimeout(function () {
      if (token === paneToken) btn.classList.remove("pressed");
    }, 420);

    var current = $("#" + activePane);
    var pane = $("#" + paneId);
    if (!current || !pane) { activePane = paneId; return; }

    if (reducedMotion) {
      clearPaneAnim(current);
      clearPaneAnim(pane);
      current.hidden = true;
      pane.hidden = false;
      activePane = paneId;
      return;
    }

    var down = tabOrderIndex(paneId) >= tabOrderIndex(activePane);
    var outCls = down ? "sc-out-up" : "sc-out-down";
    var inCls = down ? "sc-in-down" : "sc-in-up";

    clearPaneAnim(current);
    clearPaneAnim(pane);
    current.classList.add(outCls);

    setTimeout(function () {
      if (token !== paneToken) return;
      current.hidden = true;
      current.classList.remove(outCls);
      activePane = paneId;

      pane.hidden = false;
      void pane.offsetWidth;   /* 重启动画 */
      pane.classList.add(inCls);
      setTimeout(function () {
        if (token !== paneToken) return;
        pane.classList.remove(inCls);
      }, 500);
    }, 340);
  }

  /* ═══════════ 快捷键面板（paneShortcuts） ═══════════
     渲染注册表全部动作（Shortcuts.groups），每行：动作名 + 当前键位 +
     「更改」（进入捕获态，按下新键位即绑定，Esc 取消）+「恢复默认」。
     绑定即时写入 localStorage（与音频设置"即时生效"一致，chat 页下次
     加载即生效）；冲突键位由 Shortcuts.set 拒绝并提示占用者。 */
  var _capturingRow      = null;   // 当前捕获态的 DOM 行

  function renderShortcutsPane() {
    var list = $("#shortcutsList");
    if (!list || !window.Shortcuts) return;
    list.innerHTML = "";
    var groups = window.Shortcuts.groups();
    groups.forEach(function (g) {
      if (!g.items.length) return;
      var title = document.createElement("div");
      title.className = "group-title";
      title.style.marginTop = "8px";
      title.textContent = g.label;
      list.appendChild(title);
      g.items.forEach(function (item) {
        var row = document.createElement("div");
        row.className = "shortcut-row";
        row.dataset.action = item.action;

        var name = document.createElement("span");
        name.className = "shortcut-name";
        name.textContent = item.label;

        var keys = document.createElement("span");
        keys.className = "shortcut-keys";
        keys.textContent = window.Shortcuts.pretty(item.keys);

        var changeBtn = document.createElement("button");
        changeBtn.type = "button";
        changeBtn.className = "btn btn-secondary btn-sm";
        changeBtn.textContent = "更改";
        changeBtn.addEventListener("click", function () {
          startCapture(row);
        });

        var resetBtn = document.createElement("button");
        resetBtn.type = "button";
        resetBtn.className = "btn btn-secondary btn-sm shortcut-reset";
        resetBtn.textContent = "恢复默认";
        resetBtn.addEventListener("click", function () {
          window.Shortcuts.reset(item.action);
          renderShortcutsPane();
          UI.toast ("✓ 已恢复默认: " + item.label, "ok");
        });

        row.appendChild(name);
        row.appendChild(keys);
        row.appendChild(changeBtn);
        row.appendChild(resetBtn);
        list.appendChild(row);
      });
    });

    var resetAll = $("#shortcutsResetAllBtn");
    if (resetAll) {
      resetAll.onclick = function () {
        window.Shortcuts.resetAll();
        renderShortcutsPane();
        UI.toast ("✓ 已恢复全部默认键位", "ok");
      };
    }
  }

  /* 捕获态：行高亮 + 提示"按下新键位…"；Esc/鼠标点击取消；
     修饰键组合按下时等待松开再判定（避免 Ctrl 按下瞬间误绑） */
  function startCapture(row     ) {
    if (_capturingRow) cancelCapture();
    _capturingRow = row;
    row.classList.add("capturing");
    var hint = $("#shortcutsHint");
    if (hint) hint.textContent = "按下新键位…（Esc 取消）";
    row.querySelector(".shortcut-keys").textContent = "…";

    var done = false;
    function finish() {
      if (done) return;
      done = true;
      document.removeEventListener("keydown", onKey, true);
      document.removeEventListener("mousedown", onMouse, true);
      if (_capturingRow === row) _capturingRow = null;
      var h2 = $("#shortcutsHint");
      if (h2) h2.textContent = "";
      row.classList.remove("capturing");
      renderShortcutsPane();   // 恢复键位显示
    }
    function onKey(e     ) {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); finish(); return; }
      if (e.key === "Control" || e.key === "Alt" || e.key === "Shift" || e.key === "Meta") return; // 等修饰键松开
      e.preventDefault();
      e.stopPropagation();
      var spec = window.Shortcuts.normalizeKey(e);
      if (!spec || spec === "+") { finish(); return; }
      var action = row.dataset.action;
      var err = window.Shortcuts.set(action, spec);
      if (err) {
        UI.toast ("✗ " + err, "err");
      } else {
        UI.toast ("✓ 已绑定 " + window.Shortcuts.label(action) + ": " + window.Shortcuts.pretty(spec), "ok");
      }
      finish();
    }
    function onMouse(e     ) {
      if (!row.contains(e.target)) finish();
    }
    document.addEventListener("keydown", onKey, true);
    document.addEventListener("mousedown", onMouse, true);
  }

  function cancelCapture() {
    if (!_capturingRow) return;
    _capturingRow.classList.remove("capturing");
    _capturingRow = null;
    var hint = $("#shortcutsHint");
    if (hint) hint.textContent = "";
  }

  /* ═══════════ 用户知识库（paneLibrary） ═══════════
     Library/user/ 下的 .md/.txt 文件管理：文件名即内容概括，AI 据此判断
     是否 read_library_file。列表/上传/删除/重命名/预览，操作即时生效
     （AI 每轮请求都会重建文件清单，无需保存配置）。 */

  function libFmtSize(n     ) {
    if (!(n >= 0)) return "";
    if (n < 1024) return n + " B";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
    return (n / 1024 / 1024).toFixed(2) + " MB";
  }

  function libFmtTime(ms     ) {
    try { return new Date(ms).toLocaleDateString(); } catch (e) { return ""; }
  }

  /* 当前用户文件名（小写，Windows 文件名不区分大小写），上传时判断同名覆盖 */
  var libUserNames        = [];

  function libFileRow(f     , builtin     ) {
    var row = document.createElement("div");
    row.className = "lib-file-row";
    row.dataset.name = f.name;

    var name = document.createElement("span");
    name.className = "lib-file-name";
    name.textContent = f.name;
    name.title = f.name;
    row.appendChild(name);

    if (builtin) {
      var tag = document.createElement("span");
      tag.className = "lib-file-tag";
      tag.textContent = "内置";
      row.appendChild(tag);
    }

    var meta = document.createElement("span");
    meta.className = "lib-file-meta dim";
    meta.textContent = libFmtSize(f.size) + " · " + libFmtTime(f.modified);
    row.appendChild(meta);

    var actions = document.createElement("span");
    actions.className = "lib-file-actions";

    var previewBtn = document.createElement("button");
    previewBtn.type = "button";
    previewBtn.className = "btn btn-secondary btn-sm action-preview";
    previewBtn.textContent = "预览";
    actions.appendChild(previewBtn);

    if (!builtin) {
      var renameBtn = document.createElement("button");
      renameBtn.type = "button";
      renameBtn.className = "btn btn-secondary btn-sm action-rename";
      renameBtn.textContent = "重命名";
      actions.appendChild(renameBtn);

      var delBtn = document.createElement("button");
      delBtn.type = "button";
      delBtn.className = "btn btn-danger btn-sm action-del";
      delBtn.textContent = "删除";
      actions.appendChild(delBtn);
    }

    row.appendChild(actions);
    return row;
  }

  function libEmptyHint(text     ) {
    var empty = document.createElement("div");
    empty.className = "dim lib-file-empty";
    empty.textContent = text;
    return empty;
  }

  function renderLibraryLists(data     ) {
    var list = $("#libFileList");
    var builtinList = $("#libBuiltinList");
    if (!list || !builtinList) return;
    list.innerHTML = "";
    builtinList.innerHTML = "";
    libUserNames = [];

    var user = data.user || [];
    var builtin = data.builtin || [];
    var status = $("#libStatus");
    if (status) {
      status.textContent = user.length
        ? "共 " + user.length + " 个自定义文件"
        : "尚未添加自定义知识文件";
    }

    if (!user.length) {
      list.appendChild(libEmptyHint("还没有自定义知识文件，点击「上传知识文件」添加（支持多选）。"));
    }
    user.forEach(function (f     ) {
      libUserNames.push((f.name || "").toLowerCase());
      list.appendChild(libFileRow(f, false));
    });

    if (!builtin.length) {
      builtinList.appendChild(libEmptyHint("未检测到内置知识文件（Library 目录缺失或为空）。"));
    }
    builtin.forEach(function (f     ) {
      builtinList.appendChild(libFileRow(f, true));
    });
  }

  function loadLibraryFiles() {
    return UI.getJSON("/api/library/files").then(renderLibraryLists).catch(function (e) {
      UI.toast ("✗ 加载知识库文件列表失败: " + e.message, "err");
    });
  }

  /* 预览：内嵌展开在行下方，再点收起（每次展开重新拉取，保证最新内容） */
  function toggleLibPreview(row     ) {
    var scope = row.parentElement && row.parentElement.id === "libBuiltinList" ? "builtin" : "user";
    var next = row.nextElementSibling;
    if (next && next.classList.contains("lib-preview")) {
      next.remove();
      return;
    }
    var url = "/api/library/files/content?scope=" + scope + "&name=" + encodeURIComponent(row.dataset.name);
    UI.getJSON(url).then(function (j     ) {
      if (!row.isConnected) return;
      var nx = row.nextElementSibling;
      if (nx && nx.classList.contains("lib-preview")) nx.remove();
      var pre = document.createElement("pre");
      pre.className = "lib-preview";
      pre.textContent = (j.content || "") + (j.truncated ? "\n\n…（内容过长，已截断显示）" : "");
      row.after(pre);
    }).catch(function (e) {
      UI.toast ("✗ 预览失败: " + e.message, "err");
    });
  }

  function deleteLibFile(row     ) {
    var name = row.dataset.name;
    UI.confirm ({
      title: "删除知识文件",
      text: "确定删除「" + name + "」？此操作不可恢复。",
      okText: "删除",
    }).then(function (ok) {
      if (!ok) return;
      UI.delJSON("/api/library/files?name=" + encodeURIComponent(name)).then(function () {
        UI.toast ("✓ 已删除 " + name, "ok");
        loadLibraryFiles();
      }).catch(function (e) {
        UI.toast ("✗ 删除失败: " + e.message, "err");
      });
    });
  }

  function renameLibFile(row     ) {
    var name = row.dataset.name;
    /* UI.prompt 应用内弹窗：原生 window.prompt 外观突兀且阻塞渲染 */
    UI.prompt ("重命名知识文件（文件名即内容概括，供 AI 判断是否调用）：", name)
      .then(function (input) {
        if (input === null) return;
        var to = input.trim();
        if (!to || to === name) return;
        UI.postJSON("/api/library/files/rename", { from: name, to: to }).then(function () {
          UI.toast ("✓ 已重命名为 " + to, "ok");
          loadLibraryFiles();
        }).catch(function (e) {
          UI.toast ("✗ 重命名失败: " + e.message, "err");
        });
      });
  }

  /* 逐个串行上传（单文件失败不阻断后续），完成后统一刷新列表 */
  function uploadLibFiles(files     ) {
    var pending = Array.prototype.slice.call(files || []);
    var upload = function (f     ) {
      return Promise.resolve(f.arrayBuffer ? f.arrayBuffer() : new Promise(function (res, rej) {
        var fr = new FileReader();
        fr.onload = function () { res(fr.result); };
        fr.onerror = function () { rej(new Error("读取文件失败")); };
        fr.readAsArrayBuffer(f);
      })).then(function (buf) {
        return fetch("/api/library/files?name=" + encodeURIComponent(f.name), {
          method: "POST",
          headers: { "Content-Type": "application/octet-stream" },
          body: buf,
        }).then(function (r) {
          if (!r.ok) {
            return r.json().then(function (j) {
              throw new Error(j.detail || ("HTTP " + r.status));
            }, function () {
              throw new Error("HTTP " + r.status);
            });
          }
          UI.toast ("✓ 已上传 " + f.name, "ok");
        });
      }).catch(function (e) {
        UI.toast ("✗ 上传「" + f.name + "」失败: " + e.message, "err");
      });
    };
    var next = function () {
      var f = pending.shift();
      if (!f) { loadLibraryFiles(); return; }
      /* 同名覆盖要问一句，问完再继续队列（不能像 window.confirm 那样原地阻塞） */
      if (libUserNames.indexOf(f.name.toLowerCase()) >= 0) {
        UI.confirm ({
          title: "文件已存在",
          text: "已存在同名文件「" + f.name + "」，覆盖？",
          okText: "覆盖",
        }).then(function (ok) {
          return ok ? upload(f).then(next) : next();
        });
        return;
      }
      upload(f).then(next);
    };
    next();
  }

  function bindLibraryPane() {
    if (!$("#libUploadBtn")) return;
    $("#libUploadBtn").addEventListener("click", function () {
      $("#libFileInput").click();
    });
    $("#libFileInput").addEventListener("change", function () {
      uploadLibFiles(this.files);
      this.value = "";   // 允许重复选择同一文件
    });
    $("#libOpenDirBtn").addEventListener("click", function () {
      UI.postJSON("/api/library/open", {}).catch(function (e) {
        UI.toast ("✗ 打开文件夹失败: " + e.message, "err");
      });
    });
    $("#libFileList").addEventListener("click", function (e) {
      var row = e.target .closest (".lib-file-row");
      if (!row) return;
      if (e.target .classList .contains("action-preview")) toggleLibPreview(row);
      else if (e.target .classList .contains("action-del")) deleteLibFile(row);
      else if (e.target .classList .contains("action-rename")) renameLibFile(row);
    });
    $("#libBuiltinList").addEventListener("click", function (e) {
      var row = e.target .closest (".lib-file-row");
      if (row && e.target .classList .contains("action-preview")) toggleLibPreview(row);
    });
  }

  /* ═══════════ 关于页专属特效（paneAbout） ═══════════
     纯白球 + mix-blend-mode: difference：浅色主题呈黑球白字、深色主题呈
     浅色球洞，随主题自动翻转。三态：
     - 文字：48px 圆球跟随光标；
     - Logo：26px 小球，::after 三层光影（深核/暗晕/隆起高光）定位到光标处
       ——触点局部凹陷，整块仅 0.985 微缩；
     - 按钮：瞬间"吸附"——球弹到按钮中心、变形为按钮矩形（含圆角），
       覆盖处整体反色；离开按钮恢复跟随。 */
  function initAboutFx() {
    var pane = $("#paneAbout");
    if (!pane) return;

    /* 系统开启"减弱动态效果"时跳过整个特效：
       CSS 侧只关了 transition，JS 侧不跳过的话光标仍会被球体盖住
       （cursor 样式）且球体跟随导致整屏反色重绘 */
    if (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      return;
    }

    var ball = document.createElement("div");
    ball.className = "about-ball";
    pane.appendChild(ball);

    var logoWrap      = pane.querySelector(".about-logo-wrap");
    var raf      = null;
    var mx = 0, my = 0, size = 48;
    var snapBtn      = null;

    function paint() {
      raf = null;
      if (snapBtn) return;   // 吸附态：球钉在按钮中心，不跟光标
      ball.style.transform = "translate3d(" + mx + "px," + my + "px,0) translate(-50%,-50%)";
      ball.style.width = size + "px";
      ball.style.height = size + "px";
    }

    function releaseSnap() {
      if (!snapBtn) return;
      snapBtn = null;
      ball.classList.remove("snap");
      ball.style.borderRadius = "50%";
    }

    function snapTo(btn     ) {
      if (snapBtn === btn) return;
      snapBtn = btn;
      /* 几何测量只在"进入新按钮"时做一次（光标停在按钮上移动期间
         按钮几何不变）：此前每次 pointermove 都同步
         getBoundingClientRect + getComputedStyle，强制布局/样式重算，
         光标在 About 页按钮上快速移动时产生每帧抖动 */
      var r = btn.getBoundingClientRect();
      var cs = getComputedStyle(btn);
      ball.classList.add("snap");
      ball.style.transform = "translate3d(" + (r.left + r.width / 2) + "px," + (r.top + r.height / 2) + "px,0) translate(-50%,-50%)";
      ball.style.width = Math.round(r.width) + "px";
      ball.style.height = Math.round(r.height) + "px";
      ball.style.borderRadius = cs.borderRadius || "4px";
    }

    var TILT_MAX = 18;   // 3D 倾斜最大角度（度）：触点一侧明显下沉
    var logoPressed = false;   // 指针在 logo 上的判定状态（按位置推导，不依赖边界事件）

    function moveLogoDent(x     , y     ) {
      if (!logoWrap) return;
      logoPressed = true;
      var lr = logoWrap.getBoundingClientRect();
      /* 钳制到 [0,1]：光标贴边越界时（凸出条带上仍可能派发 move），
         倾斜角与凹陷位置不得越界放大 */
      var px = Math.min(1, Math.max(0, (x - lr.left) / lr.width));
      var py = Math.min(1, Math.max(0, (y - lr.top) / lr.height));
      logoWrap.style.setProperty("--dx", (px * 100).toFixed(1) + "%");
      logoWrap.style.setProperty("--dy", (py * 100).toFixed(1) + "%");
      /* rotateX 正值 = 上边向屏幕里沉、rotateY 正值 = 右边向屏幕里沉
         （CSS 坐标 Y 轴朝下，rotateY 正角把 +x 边推向 -z）：
         光标在哪一侧，哪一侧朝屏幕里陷进去 */
      logoWrap.style.setProperty("--rx", ((0.5 - py) * TILT_MAX).toFixed(2) + "deg");
      logoWrap.style.setProperty("--ry", ((px - 0.5) * TILT_MAX).toFixed(2) + "deg");
    }

    function clearLogoDent() {
      if (!logoWrap) return;
      logoPressed = false;
      logoWrap.classList.remove("pressed");
      ["--dx", "--dy", "--rx", "--ry"].forEach(function (p) {
        logoWrap .style.removeProperty(p);
      });
    }

    pane.addEventListener("pointermove", function (e) {
      mx = e.clientX; my = e.clientY;
      var t = e.target;
      var btn = t .closest ? t .closest (".about-link, .btn") : null;
      var onLogo = t .closest ? t .closest (".about-logo-wrap") : null;
      if (btn) {
        /* 只在目标按钮变化时重测几何（snapTo 内 snapBtn===btn 直接
           return）：光标在按钮上连续移动时不再每帧 getBoundingClientRect
           + getComputedStyle，消除布局抖动 */
        snapTo(btn);
      } else {
        releaseSnap();
        if (onLogo) {
          size = 26;
          moveLogoDent(mx, my);
        } else {
          size = 48;
          /* 指针已在 logo 外但下沉态仍在：wrap 的 pointerleave 在部分
             环境（3D 变换/WebView2）可能丢失——按当前位置强制复位，
             否则光标移出后 logo 卡在下沉态、球卡在 26px（"移不出来"）。
             以 class 为准（不依赖与 class 同步维护的 flag） */
          if (logoWrap.classList.contains("pressed")) clearLogoDent();
        }
        if (!raf) raf = requestAnimationFrame(paint);
      }
    });

    pane.addEventListener("pointerenter", function () { ball.classList.add("on"); });
    pane.addEventListener("pointerleave", function () {
      ball.classList.remove("on");
      releaseSnap();
      clearLogoDent();
    });
    /* 窗口级兜底：pane 的 pointerleave 在个别场景（滚动导致内容从指针下
       移走、系统级指针捕获）可能不派发——按指针实际位置推导，指针一旦
       出了面板范围就整体复位，球与下沉态都不可能卡住 */
    window.addEventListener("pointermove", function (e     ) {
      if (e.clientX == null) return;
      /* 廉价门控：球不在活动态、无下沉、无吸附时没有任何可卡住的状态，
         不做几何读取（About 页指针移动频繁，避免每帧强制布局） */
      if (!ball.classList.contains("on") && !logoWrap.classList.contains("pressed") && !snapBtn) return;
      var pr = pane.getBoundingClientRect();
      var inside = e.clientX >= pr.left && e.clientX <= pr.right &&
        e.clientY >= pr.top && e.clientY <= pr.bottom;
      if (inside) return;
      if (ball.classList.contains("on") || logoPressed || snapBtn) {
        ball.classList.remove("on");
        releaseSnap();
        clearLogoDent();
      }
    });
    /* 滚轮滚动时指针不动、没有指针事件，但内容会从指针下移走：
       logo/按钮/面板与指针的相对位置全部失效——按最后已知光标位置
       重新推导，卡住的下沉态/吸附态/球显隐随之复位 */
    window.addEventListener("scroll", function () {
      var pr = pane.getBoundingClientRect();
      var inside = mx >= pr.left && mx <= pr.right && my >= pr.top && my <= pr.bottom;
      if (!inside) {
        ball.classList.remove("on");
        releaseSnap();
        clearLogoDent();
        return;
      }
      var wrapEl = logoWrap;
      if (!wrapEl || (!wrapEl.classList.contains("pressed") && !snapBtn)) return;
      var el = document.elementFromPoint(mx, my);
      var stillOnLogo = !!(el && el.closest && el.closest(".about-logo-wrap"));
      if (wrapEl.classList.contains("pressed") && !stillOnLogo) clearLogoDent();
      var stillOnBtn = !!(el && el.closest && snapBtn && el.closest(".about-link, .btn") === snapBtn);
      if (snapBtn && !stillOnBtn) releaseSnap();
    }, { passive: true });

    function onLogoNow(el     ) {
      return !!(el && el.closest && el.closest(".about-logo-wrap"));
    }

    function btnUnderPoint() {
      var el = document.elementFromPoint(mx, my);
      return !!(el && el.closest && el.closest(".about-link, .btn"));
    }
    function isSameRect(btn     ) {
      return document.contains(btn);
    }

    if (logoWrap) {
      logoWrap.addEventListener("pointerenter", function () { logoWrap .classList.add("pressed"); });
      logoWrap.addEventListener("pointerleave", clearLogoDent);
    }
  }

  /* 左上角返回按钮。优先 history.back()：它保留前进/后退栈，用户再点前进
     能回到设置页。只在"确实有一页站内来路"时才用——冷启动直接打开
     settings.html（历史里没有本站页面）时退出去会离开应用，那种情况兜底
     跳对话页（应用主入口）。 */
  function bindBackButton() {
    var btn = $("#settingsBackBtn");
    if (!btn) return;
    btn.addEventListener("click", function () {
      var cameFromApp = false;
      try {
        cameFromApp = !!document.referrer &&
          document.referrer.indexOf(location.origin + "/") === 0 &&
          document.referrer.indexOf("/settings.html") < 0;
      } catch (e) {}
      try { sessionStorage.setItem("ai-midi-nav-dir", "back"); } catch (e) {}
      if (cameFromApp && history.length > 1) {
        history.back();
        return;
      }
      location.href = "/chat.html";
    });
  }

  function init() {
    /* 左上角返回：回到"打开设置之前那一页"。走 history.back() 而不是写死
       目标页，是因为设置可以从快捷操作页、对话页、甚至对话页的深链进来。
       加上 data-dir 的方向标记，返回时按「后退」播滑动动画。 */
    bindBackButton();

    /* 标签页：默认激活「连接」；支持 ?tab=paneXXX 深链（chat.html 的
       「⌨ 快捷键」入口直达快捷键面板） */
    var tabFromUrl = null;
    try {
      tabFromUrl = new URLSearchParams(location.search).get("tab");
    } catch (e) {}
    var initialPane = (tabFromUrl && $("#" + tabFromUrl)) ? tabFromUrl : "paneConnect";
    activatePane(initialPane);
    UI.qsa(".settings-tab", $(".settings-tabs")).forEach(function (t) {
      t.addEventListener("click", function () { switchPane(t, t.dataset.pane); });
    });
    if (initialPane === "paneShortcuts") {
      renderShortcutsPane();
    }
    if (initialPane === "paneLibrary") {
      loadLibraryFiles();
    }
    bindLibraryPane();
    initAboutFx();

    /* 检查更新：调 /api/update/check，有新版本走 update.js 的通知卡，
       没有就地 toast */
    if ($("#checkUpdateBtn")) {
      $("#checkUpdateBtn").addEventListener("click", function () {
        var btn = this                     ;
        if (btn.disabled) return;
        btn.disabled = true;
        btn.textContent = "⟳ 检查中…";
        var done = function () {
          btn.disabled = false;
          btn.textContent = "🔄 检查更新";
        };
        var onResult = function (r     ) {
          done();
          if (r && r.available) {
            UI.toast ("✓ 发现新版本 " + (r.latest || "") + "，请留意更新提示", "ok");
          } else {
            UI.toast ("✓ 已是最新版本" + (r && r.current ? "（v" + r.current + "）" : ""), "ok");
          }
        };
        try {
          var up = (window       ).Update;
          if (up && typeof up.checkNow === "function") {
            up.checkNow().then(onResult, function (e     ) {
              done();
              UI.toast ("✗ 检查更新失败：" + (e && e.message || e), "err");
            });
          } else {
            UI.getJSON     ("/api/update/check").then(onResult, function (e     ) {
              done();
              UI.toast ("✗ 检查更新失败：" + (e && e.message || e), "err");
            });
          }
        } catch (err     ) {
          done();
          UI.toast ("✗ 检查更新失败：" + (err && err.message || err), "err");
        }
      });
    }

    /* 提交 Issue：跳转 GitHub issue 新建页（系统默认浏览器） */
    if ($("#issueBtn")) {
      $("#issueBtn").addEventListener("click", function () {
        if (window.UI && window.UI.openExternal) {
          UI.openExternal ("https://github.com/abab996/AI_MIDI/issues/new");
        }
      });
    }

    /* 关于页外链（官网/GitHub/License）：统一走 /api/open-url 白名单，
       由系统默认浏览器打开（Wails 内 WebView 直接导航会离开应用界面） */
    UI.qsa(".about-links a[data-ext]").forEach(function (a) {
      a.addEventListener("click", function (e) {
        e.preventDefault();
        UI.openExternal (a.href);
      });
    });

    /* 预设和模型目录先到，再填表，手填 ID 才能对上上下文提示 */
    Promise.all([
      UI.getJSON("/api/provider-presets").catch(function () { return { presets: [] }; }),
      UI.getJSON("/api/model-profiles").catch(function () { return { profiles: [] }; }),
      UI.getJSON("/api/settings"),
    ]).then(function (all     ) {
      presets = (all[0] && all[0].presets) || [];
      profiles = (all[1] && all[1].profiles) || [];
      fillForm(all[2]);
      fillMidiSettings();
    }).catch(function (e) {
      UI.toast ("✗ 加载设置失败: " + e.message, "err");
      fillMidiSettings();
    });

    /* About 卡片：版本号（与 wails.json 同源，失败静默显示 --） */
    if ($("#aboutVersion")) {
      UI.getJSON("/api/version").then(function (v     ) {
        $("#aboutVersion").textContent = "v" + (v.version || "--");
      }).catch(function () {});
    }

    /* MIDI 控制事件 */
    if ($("#refreshMidiBtn")) {
      $("#refreshMidiBtn").addEventListener("click", function () {
        scanMidiDevices($("#midiDeviceSelect") ? $("#midiDeviceSelect").value : "auto");
        UI.toast ("✓ 已重新扫描 MIDI 端口", "ok");
      });
    }
    if ($("#midiDeviceSelect")) {
      $("#midiDeviceSelect").addEventListener("change", function () {
        bindSelectedMidiInput(this.value);
      });
    }
    if ($("#midiInputEnabled")) {
      $("#midiInputEnabled").addEventListener("change", function () {
        bindSelectedMidiInput($("#midiDeviceSelect").value);
      });
    }

    /* 显示/隐藏 API Key（详情区是固定节点，监听一次即可） */
    $("#showKeyBtn").addEventListener("click", function () {
      var keyInput      = $("#apiKey");
      var showing = keyInput.type === "text";
      keyInput.type = showing ? "password" : "text";
      this.textContent = showing ? "显示" : "隐藏";
    });

    /* 推理强度分段（全局默认值）。这个值只在"模型没配过档位"时兜底，
       改完刷新模型卡摘要让两处显示一致 */
    UI.qsa(".fn", $("#effortSelector")).forEach(function (btn) {
      btn.addEventListener("click", function () {
        UI.qsa(".fn", $("#effortSelector")).forEach(function (b) {
          b.classList.toggle("active", b === btn);
        });
        renderModelCards();
      });
    });

    UI.qsa(".fn", $("#protocolSelector")).forEach(function (btn) {
      btn.addEventListener("click", function () {
        setProtocol(btn.dataset.protocol          );
        var p = findProvider(selectedProviderId);
        if (p) {
          p.protocol = btn.dataset.protocol;
          renderHead(p);
        }
      });
    });

    /* 名称 / Base URL 改动实时反映到详情页头，不用等保存 */
    $("#provName").addEventListener("input", function () {
      var p = findProvider(selectedProviderId);
      if (!p) return;
      p.name = ($("#provName")                    ).value.trim() || p.name;
      renderHead(p);
      renderProviderList();
    });
    $("#baseUrl").addEventListener("input", function () {
      var p = findProvider(selectedProviderId);
      if (!p) return;
      p.base_url = ($("#baseUrl")                    ).value.trim();
      renderHead(p);
      renderProviderList();
    });

    /* 连接页输入字段（名称/Key/URL/路径）失焦自动保存：与开关/档位的
       即时落库语义对齐，消除"哪些字段要手动保存"的困惑。值未变化
       （Tab 路过）不发请求；输入一半的 Key 也不会再被误触开关隐式写盘
       ——blur 即触发保存，开关读到的始终是已保存值 */
    var detailSaveTimer      = null;
    ["provName", "baseUrl", "apiPath", "apiKey"].forEach(function (id) {
      var found = document.getElementById(id);
      if (!found) return;
      var input = found                    ;
      input.addEventListener("focus", function () {
        (input       )._savedValue = input.value;
      });
      input.addEventListener("blur", function () {
        if ((input       )._savedValue === input.value) return;
        (input       )._savedValue = input.value;
        if (detailSaveTimer) clearTimeout(detailSaveTimer);
        detailSaveTimer = setTimeout(function () {
          detailSaveTimer = null;
          persistProviders().catch(function () {});
        }, 500);
      });
    });

    $("#addPresetBtn").addEventListener("click", function (e) {
      e.stopPropagation();
      if (presetOpen) closePresetMenu();
      else openPresetMenu();
    });
    $("#addCustomBtn").addEventListener("click", function () {
      closePresetMenu();
      addCustom();
    });
    document.addEventListener("click", function (e) {
      if (!(e.target           ).closest || !(e.target           ).closest(".provider-rail")) closePresetMenu();
    });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape") closePresetMenu();
    });

    $("#deleteProviderBtn").addEventListener("click", function () {
      var p = findProvider(selectedProviderId);
      if (!p) return;
      var modelCount = (p.models || []).length;
      UI.confirm ({
        title: "删除供应商",
        text: "删除「" + p.name + "」？已保存的密钥会一起删除" +
          (modelCount ? "，" + modelCount + " 个可用模型的参数也一并丢弃。" : "。"),
        okText: "删除",
      }).then(function (ok) {
        if (!ok) return;
        providers = providers.filter(function (x) { return x.id !== p .id; });
        if (activeProviderId === p .id) {
          activeProviderId = "";
          activeModelId = "";
        }
        selectedProviderId = (providers[0] && providers[0].id) || "";
        discovered = [];
        renderProviderList();
        fillDetail();
        persistProviders().then(function () {
          UI.toast ("✓ 已删除供应商「" + p .name + "」", "ok");
        }).catch(function () {});
      });
    });

    /* 测试连接：与刷新列表同走 POST /api/models，但语义显式化——
       此前填完 Key 只能靠"⟳ 刷新列表"隐式验证，密钥是否有效要到
       对话发送失败才暴露 */
    $("#testConnBtn").addEventListener("click", function () {
      var btn = this                     ;
      readDetail();
      var p = findProvider(selectedProviderId);
      var status = $("#testConnStatus");
      if (!p) {
        UI.toast ("先选择或新建一个接入点", "warn");
        return;
      }
      btn.disabled = true;
      if (status) {
        status.textContent = "正在连接 " + (p.name || "接入点") + " …";
        status.className = "dim model-status";
      }
      UI.postJSON("/api/models", {
        api_key: p.api_key || "",
        base_url: p.base_url,
        api_path: p.api_path,
        protocol: p.protocol,
        provider_id: p.id,
      }).then(function (data     ) {
        var n = (data.models || []).length;
        /* 后端契约：失败原因以 message 字段带回（HTTP 仍 200、models 为空）
           ——此前只看 models 长度，不可达端点也会误报"连接成功" */
        var failed = !!(data.message && String(data.message).indexOf("✓") !== 0) || n === 0;
        if (failed) {
          /* 后端 message 自带 ✗/✓ 前缀的剥掉，避免"✗ 连接失败: ✗ 获取…"双符号 */
          var reason = String(data.message || "未获取到任何模型").replace(/^[✗✓]\s*/, "");
          if (status) {
            status.textContent = "✗ 连接失败: " + reason;
            status.className = "err model-status";
          }
          UI.toast ("✗ 连接失败: " + reason, "err");
        } else {
          if (status) {
            status.textContent = "✓ 连接成功，" + n + " 个模型可用";
            status.className = "ok model-status";
          }
          UI.toast ("✓ 连接成功，" + n + " 个模型可用", "ok");
        }
      }).catch(function (e) {
        if (status) {
          status.textContent = "✗ 连接失败: " + e.message;
          status.className = "err model-status";
        }
        UI.toast ("✗ 连接失败: " + e.message, "err");
      }).finally(function () { btn.disabled = false; });
    });

    $("#refreshModelsBtn").addEventListener("click", function () {
      var btn = this                     ;
      readDetail();
      var p = findProvider(selectedProviderId);
      if (!p) return;
      var status = $("#modelStatus");
      btn.disabled = true;
      status.textContent = "正在获取模型列表…";
      status.className = "dim";
      UI.postJSON("/api/models", {
        api_key: p.api_key || "",
        base_url: p.base_url,
        api_path: p.api_path,
        protocol: p.protocol,
        provider_id: p.id,
      }).then(function (data     ) {
        discovered = data.models || [];
        status.textContent = data.message || (discovered.length ? "勾选后点「添加可用模型」" : "未获取到模型，可以手填 ID");
        status.className = discovered.length ? "ok" : "err";
        renderDiscovered();
        if (discovered.length) UI.toast ("✓ 已获取 " + discovered.length + " 个模型", "ok");
        else UI.toast ("✗ " + (data.message || "未获取到模型"), "err");
      }).catch(function (e) {
        status.textContent = "✗ 获取失败: " + e.message;
        status.className = "err";
      }).finally(function () { btn.disabled = false; });
    });

    $("#addDiscoveredBtn").addEventListener("click", function () {
      var p = findProvider(selectedProviderId);
      if (!p) return;
      var have      = {};
      p.models = p.models || [];
      p.models.forEach(function (m     ) { have[m.id] = true; });
      var added = 0;
      UI.qsa("input[type=checkbox]", $("#discoveredList")).forEach(function (el) {
        var box = el                    ;
        if (!box.checked || box.disabled) return;
        var id = box.dataset.mid || "";
        if (!id || have[id]) return;
        p.models.push(blankModel(id));
        have[id] = true;
        added++;
      });
      if (!added) {
        UI.toast ("先勾选要添加的模型", "warn");
        return;
      }
      renderDiscovered();
      renderModelCards();
      persistProviders();
    });

    function addManual() {
      var p = findProvider(selectedProviderId);
      var input = $("#manualModelId")                    ;
      if (!p || !input) return;
      var id = input.value.trim();
      if (!id) return;
      p.models = p.models || [];
      if (p.models.some(function (m     ) { return m.id === id; })) {
        UI.toast ("该模型已在可用列表中", "warn");
        return;
      }
      var model = blankModel(id);
      p.models.push(model);
      input.value = "";
      renderModelCards();
      persistProviders().then(function () {
        if (model._context) UI.toast ("已按目录填充「" + id + "」的参数", "ok");
      });
    }
    $("#addManualModelBtn").addEventListener("click", addManual);
    $("#manualModelId").addEventListener("keydown", function (e) {
      if (e.key === "Enter") { e.preventDefault(); addManual(); }
    });

    /* 保存 */
    $("#saveBtn").addEventListener("click", function () {
      var btn = this;
      btn.disabled = true;
      saveMidiSettings(collectMidiSettings());
      UI.putJSON("/api/settings", collect()).then(function (data     ) {
        var st = $("#saveStatus");
        st.textContent = data.message;
        st.style.color = "var(--color-primary)";
        UI.toast (data.message, "ok");
      }).catch(function (e) {
        var st = $("#saveStatus");
        st.textContent = "✗ 保存失败: " + e.message;
        st.style.color = "var(--color-danger)";
        UI.toast ("✗ " + e.message, "err");
      }).finally(function () {
        btn.disabled = false;
      });
    });

    /* 恢复默认只动生成页。供应商、密钥和可用模型留在连接页里。 */
    $("#resetBtn").addEventListener("click", function () {
      UI.confirm ({
        title: "恢复默认生成参数",
        text: "只重置生成参数（最大长度、推理强度、thinking）。供应商和 API Key 保持不变。",
        okText: "恢复默认",
      }).then(function (ok) {
        if (!ok) return;
        $("#maxTokens").value = "";
        $("#maxCompletion").value = "";
        $("#thinkingEnabled").checked = true;
        UI.qsa(".fn", $("#effortSelector")).forEach(function (b) {
          b.classList.toggle("active", b.dataset.effort === "max");
        });
        saveMidiSettings(DEFAULT_MIDI_SETTINGS);
        fillMidiSettings();
        persistProviders().then(function () {
          UI.toast ("✓ 已恢复默认生成参数", "ok");
        }).catch(function () {});
      });
    });
  }

  document.addEventListener("DOMContentLoaded", init);
})();
