// 分屏布局树:叶子是终端 pane,内部节点是二分 split。
// 所有操作都是不可变更新,返回新树。

export type SplitDir = 'row' | 'column'; // row: 左右分屏; column: 上下分屏

export interface LeafNode {
    kind: 'leaf';
    id: string;
}

export interface SplitNode {
    kind: 'split';
    dir: SplitDir;
    ratio: number; // 0..1,第一个子树占比
    a: LayoutNode;
    b: LayoutNode;
}

export type LayoutNode = LeafNode | SplitNode;

export const leaf = (id: string): LayoutNode => ({
    kind: 'leaf',
    id,
});

// 把 paneId 叶子替换为 split(该叶子,新叶子),新 pane 获得焦点语义由调用方处理。
export function splitLeaf(node: LayoutNode, paneId: string, dir: SplitDir, newId: string): LayoutNode {
    if (node.kind === 'leaf') {
        if (node.id !== paneId) return node;
        return { kind: 'split', dir, ratio: 0.5, a: node, b: leaf(newId) };
    }
    return {
        ...node,
        a: splitLeaf(node.a, paneId, dir, newId),
        b: splitLeaf(node.b, paneId, dir, newId),
    };
}

// 删除叶子;split 只剩一个子节点时上提。返回 null 表示树已空。
export function removeLeaf(node: LayoutNode, paneId: string): LayoutNode | null {
    if (node.kind === 'leaf') {
        return node.id === paneId ? null : node;
    }
    const a = removeLeaf(node.a, paneId);
    const b = removeLeaf(node.b, paneId);
    if (a === null) return b;
    if (b === null) return a;
    return { ...node, a, b };
}

// 按深度优先顺序取第一个叶子,用于"无聚焦 pane 时拆分目标"。
export function firstLeaf(node: LayoutNode): LeafNode {
    let cursor: LayoutNode = node;
    while (cursor.kind === 'split') {
        cursor = cursor.a;
    }
    return cursor;
}

// 树中所有叶子的 id,按深度优先顺序。
export function allLeaves(node: LayoutNode): string[] {
    if (node.kind === 'leaf') return [node.id];
    return [...allLeaves(node.a), ...allLeaves(node.b)];
}